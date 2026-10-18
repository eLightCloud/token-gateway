package model

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// Coverage proves partition conservation for source rows; Issues additionally
// report business records missing from that source, so conservation alone
// cannot certify a bill. Only bounded references are retained in a snapshot.
type OrganizationInvoiceExpenseIssue struct {
	OriginAt      int64  `json:"origin_at,omitempty"`
	UserId        int    `json:"user_id"`
	TaskId        string `json:"task_id,omitempty"`
	Reason        string `json:"reason"`
	ReservedQuota int64  `json:"reserved_quota"`
}
type OrganizationInvoiceExpenseCoverage struct {
	WalletIncompleteUserIds []int                             `json:"wallet_incomplete_user_ids,omitempty"`
	WalletIssues            []OrganizationInvoiceExpenseIssue `json:"wallet_issues,omitempty"`
	PendingSourceRows       int64                             `json:"pending_source_rows"`
	Status                  string                            `json:"status"`
	TotalRows               int64                             `json:"total_rows"`
	TotalQuota              int64                             `json:"total_quota"`
	OrdinaryRows            int64                             `json:"ordinary_rows"`
	OrdinaryQuota           int64                             `json:"ordinary_quota"`
	TaskRows                int64                             `json:"task_rows"`
	TaskQuota               int64                             `json:"task_quota"`
	UnknownRows             int64                             `json:"unknown_rows"`
	UnknownQuota            int64                             `json:"unknown_quota"`
	PendingTasks            int64                             `json:"pending_tasks"`
	MissingTaskLogs         int64                             `json:"missing_task_logs"`
	Issues                  []OrganizationInvoiceExpenseIssue `json:"issues,omitempty"`
}

var errOrganizationInvoiceSliceCapacity = errors.New("invoice task evidence slice needs subdivision")

const organizationInvoiceTaskEvidenceLimit = 5000
const organizationInvoiceTaskEventReferenceLimit = 128

type organizationInvoiceTaskEvidence struct {
	originKnown    bool
	id             int64
	entity         string
	firstConsume   *organizationInvoiceExpenseLog
	consumes       map[string]organizationInvoiceAggregate
	truncated      bool
	userId         int
	taskId         string
	origin         int64
	model          string
	currentQuota   int64
	finalQuota     *int64
	confirmedAt    int64
	source         string
	fixed          bool
	success        bool
	failure        bool
	pending        bool
	unknown        bool
	fundingPending bool
	walletMismatch bool
	logs           []organizationInvoiceExpenseLog
}
type organizationInvoiceExpenseLog struct {
	Id        int
	UserId    int
	CreatedAt int64
	Type      int
	Quota     int64
	ModelName string
	Other     string
}

func (l organizationInvoiceExpenseLog) keysetCreatedAt() int64 { return l.CreatedAt }
func (l organizationInvoiceExpenseLog) keysetId() int64        { return int64(l.Id) }

type organizationInvoiceTaskLogMetadata struct {
	TaskId       string `json:"task_id"`
	IsTask       bool   `json:"is_task"`
	PreQuota     *int64 `json:"pre_consumed_quota"`
	ActualQuota  *int64 `json:"actual_quota"`
	BillingStage string `json:"billing_stage"`
}

func organizationInvoiceBusinessKey(userId int, taskId string) string {
	return fmt.Sprintf("%d:%s", userId, taskId)
}

// This legacy correlation pass only transfers task candidates, in bounded
// batches. It runs for missing/dirty days, never for a ready HTTP cache hit.
// A task's original charge date owns the expense and its coefficient month;
// completion evidence can arrive later without moving consumption to that day.
func loadOrganizationInvoiceTaskEvidence(ctx context.Context, org int, period OrganizationInvoicePeriod) (map[string]*organizationInvoiceTaskEvidence, error) {
	ids, err := GetOrganizationInvoiceAccountUserIds(org, period)
	if err != nil {
		return nil, err
	}
	members, err := activeAndHistoricalOrganizationMembers(org, 0)
	if err != nil {
		return nil, err
	}
	scope := organizationInvoiceTimeSlice{start: period.StartTimestamp, endExclusive: period.EndTimestamp + 1}
	result := make(map[string]*organizationInvoiceTaskEvidence)
	if len(ids) == 0 {
		return result, nil
	}
	var afterTask int64
	for {
		var tasks []Task
		if err := DB.WithContext(ctx).Select("id,user_id,task_id,created_at,quota,status,usage_pending,properties,invoice_origin_at,invoice_expense_quota,invoice_expense_confirmed_at,invoice_expense_source,invoice_expense_applied").Where("user_id IN ? AND ((invoice_origin_at >= ? AND invoice_origin_at <= ?) OR ((invoice_origin_at IS NULL OR invoice_origin_at = 0) AND created_at >= ? AND created_at <= ?))", ids, period.StartTimestamp, period.EndTimestamp, period.StartTimestamp, period.EndTimestamp).Where("id > ?", afterTask).Order("id").Limit(128).Find(&tasks).Error; err != nil {
			return nil, err
		}
		if len(tasks) == 0 {
			break
		}

		for _, t := range tasks {
			e := &organizationInvoiceTaskEvidence{id: t.ID, entity: TaskSettlementEntityTask, originKnown: t.InvoiceOriginAt > 0, userId: t.UserId, taskId: t.TaskID, origin: t.CreatedAt, model: t.Properties.OriginModelName, currentQuota: int64(t.Quota), finalQuota: t.InvoiceExpenseQuota, confirmedAt: t.InvoiceExpenseConfirmedAt, source: t.InvoiceExpenseSource, success: t.Status == TaskStatusSuccess, failure: t.Status == TaskStatusFailure, pending: t.UsagePending}
			if t.InvoiceOriginAt > 0 {
				e.origin = t.InvoiceOriginAt
			}
			e.fundingPending = t.InvoiceExpenseQuota != nil && !t.InvoiceExpenseApplied

			key := organizationInvoiceBusinessKey(t.UserId, t.TaskID)
			if _, duplicate := result[key]; duplicate {
				return nil, NewOrganizationInvoiceDataError(fmt.Errorf("ambiguous task billing identity %s", key))
			}
			result[key] = e
			if len(result) > organizationInvoiceTaskEvidenceLimit {
				return nil, errOrganizationInvoiceSliceCapacity
			}
		}
		afterTask = tasks[len(tasks)-1].ID
		if len(tasks) < 128 {
			break
		}
	}
	var mj []Midjourney
	if err := DB.WithContext(ctx).Select("id,user_id,mj_id,submit_time,quota,status,action,invoice_origin_at,invoice_expense_quota,invoice_expense_confirmed_at,invoice_expense_source,invoice_expense_applied").Where("user_id IN ? AND submit_time >= ? AND submit_time < ?", ids, period.StartTimestamp*1000, (period.EndTimestamp+1)*1000).Limit(organizationInvoiceTaskEvidenceLimit + 1).Find(&mj).Error; err != nil {
		return nil, err
	}
	if len(result)+len(mj) > organizationInvoiceTaskEvidenceLimit {
		return nil, errOrganizationInvoiceSliceCapacity
	}
	for _, t := range mj {
		name := "mj_" + strings.ToLower(t.Action)
		if t.Action == "SWAP_FACE" {
			name = "swap_face"
		}
		key := organizationInvoiceBusinessKey(t.UserId, t.MjId)
		if _, duplicate := result[key]; duplicate {
			return nil, NewOrganizationInvoiceDataError(fmt.Errorf("ambiguous task billing identity %s", key))
		}
		result[key] = &organizationInvoiceTaskEvidence{id: int64(t.Id), entity: TaskSettlementEntityMidjourney, originKnown: t.InvoiceOriginAt > 0, userId: t.UserId, taskId: t.MjId, origin: t.SubmitTime / 1000, model: name, currentQuota: int64(t.Quota), finalQuota: t.InvoiceExpenseQuota, confirmedAt: t.InvoiceExpenseConfirmedAt, source: t.InvoiceExpenseSource, fixed: true, success: t.Status == "SUCCESS", failure: t.Status == "FAILURE"}
		e := result[key]
		if t.InvoiceOriginAt > 0 {
			e.origin = t.InvoiceOriginAt
		}
		e.fundingPending = t.InvoiceExpenseQuota != nil && !t.InvoiceExpenseApplied
	}
	evidenceAt := common.GetTimestamp()
	if source, ok := ctx.Value(organizationInvoiceSourceContextKey{}).(*organizationInvoiceSource); ok {
		evidenceAt = source.evidenceAt
	}
	associationEnd := period.EndTimestamp
	for _, e := range result {
		if e.finalQuota == nil {
			associationEnd = evidenceAt
			break
		}
	}
	err = forEachOrganizationInvoiceTaskAssociation(ctx, org, period, associationEnd, func(rows []organizationInvoiceExpenseLog) error {
		for _, r := range rows {
			var m organizationInvoiceTaskLogMetadata
			if common.UnmarshalJsonStr(r.Other, &m) != nil || m.TaskId == "" {
				if r.CreatedAt < period.StartTimestamp || r.CreatedAt > period.EndTimestamp {
					continue
				}
				if len(result) >= organizationInvoiceTaskEvidenceLimit {
					return errOrganizationInvoiceSliceCapacity
				}
				key := fmt.Sprintf("%d:unclassified:%d:%d", r.UserId, r.CreatedAt, r.Id)
				e := result[key]
				if e == nil {
					e = &organizationInvoiceTaskEvidence{userId: r.UserId, origin: r.CreatedAt, model: r.ModelName, pending: true, unknown: true}
					result[key] = e
				}
				if err := collectOrganizationInvoiceTaskEvent(e, r, m, organizationInvoiceSliceOwnsConsumption(members, scope, r.UserId, r.CreatedAt)); err != nil {
					return err
				}
				continue
			}
			key := organizationInvoiceBusinessKey(r.UserId, m.TaskId)
			e, exists := result[key]
			if !exists {
				if r.CreatedAt < period.StartTimestamp || r.CreatedAt > period.EndTimestamp {
					continue
				}
				if len(result) >= organizationInvoiceTaskEvidenceLimit {
					return errOrganizationInvoiceSliceCapacity
				}
				// An orphan log is excluded from ordinary consumption too. Without its
				// original task/ownership evidence it remains an unresolved business.
				var owner Task
				queryErr := DB.WithContext(ctx).Select("id,user_id,task_id,created_at,quota,status,usage_pending,properties,invoice_origin_at,invoice_expense_quota,invoice_expense_confirmed_at,invoice_expense_source,invoice_expense_applied").Where("user_id = ? AND task_id = ?", r.UserId, m.TaskId).First(&owner).Error
				if queryErr == nil {
					e = &organizationInvoiceTaskEvidence{id: owner.ID, entity: TaskSettlementEntityTask, originKnown: owner.InvoiceOriginAt > 0, userId: owner.UserId, taskId: owner.TaskID, origin: owner.CreatedAt, model: owner.Properties.OriginModelName, currentQuota: int64(owner.Quota), finalQuota: owner.InvoiceExpenseQuota, confirmedAt: owner.InvoiceExpenseConfirmedAt, source: owner.InvoiceExpenseSource, success: owner.Status == TaskStatusSuccess, failure: owner.Status == TaskStatusFailure, pending: owner.UsagePending}
					if owner.InvoiceOriginAt > 0 {
						e.origin = owner.InvoiceOriginAt
					}
					e.fundingPending = owner.InvoiceExpenseQuota != nil && !owner.InvoiceExpenseApplied

				} else if errors.Is(queryErr, gorm.ErrRecordNotFound) {
					var mj Midjourney
					mjErr := DB.WithContext(ctx).Select("id,user_id,mj_id,submit_time,quota,status,action,invoice_origin_at,invoice_expense_quota,invoice_expense_confirmed_at,invoice_expense_source,invoice_expense_applied").Where("user_id = ? AND mj_id = ?", r.UserId, m.TaskId).First(&mj).Error
					if mjErr == nil {
						e = &organizationInvoiceTaskEvidence{id: int64(mj.Id), entity: TaskSettlementEntityMidjourney, originKnown: mj.InvoiceOriginAt > 0, userId: r.UserId, taskId: m.TaskId, origin: mj.SubmitTime / 1000, model: r.ModelName, currentQuota: int64(mj.Quota), finalQuota: mj.InvoiceExpenseQuota, confirmedAt: mj.InvoiceExpenseConfirmedAt, source: mj.InvoiceExpenseSource, fixed: true, success: mj.Status == "SUCCESS", failure: mj.Status == "FAILURE", fundingPending: mj.InvoiceExpenseQuota != nil && !mj.InvoiceExpenseApplied}
						if mj.InvoiceOriginAt > 0 {
							e.origin = mj.InvoiceOriginAt
						}
					} else if errors.Is(mjErr, gorm.ErrRecordNotFound) {
						e = &organizationInvoiceTaskEvidence{userId: r.UserId, taskId: m.TaskId, origin: r.CreatedAt, model: r.ModelName, pending: true}
					} else {
						return mjErr
					}
				} else {
					return queryErr
				}
				result[key] = e
			}
			if err := collectOrganizationInvoiceTaskEvent(e, r, m, organizationInvoiceSliceOwnsConsumption(members, scope, r.UserId, r.CreatedAt)); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	var fixedCandidates []int64
	byId := make(map[int64]*organizationInvoiceTaskEvidence)
	for _, e := range result {
		if e.entity == TaskSettlementEntityTask && e.id > 0 && e.finalQuota == nil && e.success && e.origin >= period.StartTimestamp && e.origin <= period.EndTimestamp {
			fixedCandidates = append(fixedCandidates, e.id)
			byId[e.id] = e
		}
	}
	for start := 0; start < len(fixedCandidates); start += 128 {
		var rows []Task
		if err := DB.WithContext(ctx).Select("id,private_data").Where("id IN ?", fixedCandidates[start:min(start+128, len(fixedCandidates))]).Find(&rows).Error; err != nil {
			return nil, err
		}
		for _, t := range rows {
			byId[t.ID].fixed = t.PrivateData.BillingContext != nil && t.PrivateData.BillingContext.PerCallBilling
		}
	}
	for _, e := range result {
		if err := resolveOrganizationInvoiceTaskExpense(e, evidenceAt); err != nil {
			return nil, err
		}
	}
	return result, nil
}

func resolveOrganizationInvoiceTaskExpense(e *organizationInvoiceTaskEvidence, evidenceAt int64) error {
	initial := e.firstConsume
	var adjusted *int64
	var adjustedPre *int64
	var hasWholeRefund bool
	for i := range e.logs {
		l := &e.logs[i]
		var m organizationInvoiceTaskLogMetadata
		if err := common.UnmarshalJsonStr(l.Other, &m); err != nil {
			e.pending = true
			continue
		}
		if l.Type == LogTypeRefund && l.Quota < 0 && l.Quota != math.MinInt64 {
			l.Quota = -l.Quota
		}
		if l.Quota < 0 {
			return NewOrganizationInvoiceDataError(errors.New("negative task billing quota"))
		}
		if m.BillingStage == "reservation" {
			continue
		}
		if m.ActualQuota != nil {
			if m.PreQuota == nil || *m.ActualQuota < 0 || *m.PreQuota < 0 || *m.ActualQuota > common.MaxWalletQuota || *m.PreQuota > common.MaxWalletQuota {
				e.pending = true
				continue
			}
			delta := *m.ActualQuota - *m.PreQuota
			if (delta > 0 && l.Type == LogTypeConsume && delta == l.Quota) || (delta < 0 && l.Type == LogTypeRefund && -delta == l.Quota) {
				if adjusted != nil && *adjusted != *m.ActualQuota {
					e.pending = true
				} else {
					v := *m.ActualQuota
					adjusted = &v
					adjustedPre = m.PreQuota
				}
			} else {
				e.pending = true
			}
		} else if l.Type == LogTypeConsume && (initial == nil || l.CreatedAt < initial.CreatedAt) {
			initial = l
		} else if l.Type == LogTypeRefund && m.PreQuota == nil {
			hasWholeRefund = true
		}
	}
	if initial != nil {
		e.origin = initial.CreatedAt
		e.originKnown = true
		e.model = initial.ModelName
	}
	if e.finalQuota != nil && e.confirmedAt <= evidenceAt {
		if *e.finalQuota < 0 || *e.finalQuota > common.MaxWalletQuota {
			return NewOrganizationInvoiceDataError(errors.New("invalid confirmed task expense"))
		}
		e.pending = !e.fundingPending && e.currentQuota != *e.finalQuota
		return nil
	}
	if e.finalQuota != nil && e.confirmedAt > evidenceAt {
		e.pending = true
	}
	e.finalQuota = nil
	if e.truncated {
		e.pending = true
	}
	if e.pending {
		return nil
	}
	if adjusted != nil && initial != nil {
		pre := initial.Quota
		for _, l := range e.logs {
			var m organizationInvoiceTaskLogMetadata
			if common.UnmarshalJsonStr(l.Other, &m) != nil || m.BillingStage != "reservation" || m.ActualQuota == nil {
				continue
			}
			delta := l.Quota
			if l.Type == LogTypeRefund {
				delta = -delta
			}
			var err error
			pre, err = addOrganizationInvoiceSignedQuota(pre, delta)
			if err != nil {
				return err
			}
		}
		if adjustedPre == nil || *adjustedPre != pre {
			e.pending = true
			return nil
		}
	}

	if adjusted != nil && initial != nil && *adjusted == e.currentQuota && (e.success || e.failure) {
		// Verify the complete charge/refund chain, not just actual_quota in a
		// single JSON document. No historical repricing is performed here.
		var net int64
		for _, l := range e.logs {
			delta := l.Quota
			if l.Type == LogTypeRefund {
				delta = -delta
			}
			var err error
			net, err = addOrganizationInvoiceSignedQuota(net, delta)
			if err != nil {
				return err
			}
		}
		e.finalQuota = adjusted
		e.source = "legacy_adjustment_chain"
		e.walletMismatch = net != *adjusted
	} else if e.failure && e.currentQuota == 0 && hasWholeRefund && initial != nil {
		var net int64
		for _, l := range e.logs {
			delta := l.Quota
			if l.Type == LogTypeRefund {
				delta = -delta
			}
			var err error
			net, err = addOrganizationInvoiceSignedQuota(net, delta)
			if err != nil {
				return err
			}
		}
		if net != 0 {
			e.pending = true
			return nil
		}
		v := int64(0)
		e.finalQuota = &v
		e.source = "legacy_full_refund"
	} else if e.fixed && e.success && initial != nil && initial.Quota == e.currentQuota && len(e.logs) == 1 {
		v := e.currentQuota
		e.finalQuota = &v
		e.source = "legacy_fixed_fee"
	}
	return nil
}

func reconcileOrganizationInvoiceExpenseSlice(ctx context.Context, org int, slice organizationInvoiceTimeSlice, all []organizationInvoiceAggregate) ([]organizationInvoiceAggregate, OrganizationInvoiceExpenseCoverage, error) {
	coverage := OrganizationInvoiceExpenseCoverage{Status: "complete"}
	cells := make(map[organizationInvoiceCellKey]*organizationInvoiceAggregate)
	month := time.Unix(slice.start, 0).In(organizationInvoiceLocation)
	monthKey := month.Year()*100 + int(month.Month())
	for _, a := range all {
		copy := a
		cells[organizationInvoiceCellKey{userId: a.UserId, modelName: a.ModelName, periodMonth: a.PeriodMonth}] = &copy
		if err := addOrganizationInvoiceQuota(&coverage.TotalQuota, a.TotalQuota); err != nil {
			return nil, coverage, err
		}
		if err := addOrganizationInvoiceQuota(&coverage.TotalRows, a.RequestCount); err != nil {
			return nil, coverage, err
		}
	}
	source, hasSource := ctx.Value(organizationInvoiceSourceContextKey{}).(*organizationInvoiceSource)
	var evidence map[string]*organizationInvoiceTaskEvidence
	if hasSource && source.expensePeriod.StartTimestamp == slice.start && source.expensePeriod.EndTimestamp == slice.endExclusive-1 {
		evidence = source.expenses
	}
	if evidence == nil {
		p := OrganizationInvoicePeriod{StartTimestamp: slice.start, EndTimestamp: slice.endExclusive - 1, StartDate: month.Format("2006-01-02"), EndDate: month.Format("2006-01-02")}
		var err error
		evidence, err = loadOrganizationInvoiceTaskEvidence(ctx, org, p)
		if err != nil {
			return nil, coverage, err
		}
		if hasSource {
			source.expenses = evidence
			source.expensePeriod = p
		}
	}
	members, err := activeAndHistoricalOrganizationMembers(org, 0)
	if err != nil {
		return nil, coverage, err
	}
	belongs := func(uid int, at int64) bool {
		for _, m := range members {
			lo, hi, ok := organizationInvoiceMembershipSliceBounds(m, slice)
			if ok && m.UserId == uid && at >= lo && at < hi {
				return true
			}
		}
		return false
	}
	for _, e := range evidence {
		for modelName, raw := range e.consumes {
			key := organizationInvoiceCellKey{userId: e.userId, modelName: modelName, periodMonth: monthKey}
			cell := cells[key]
			if cell == nil || cell.TotalQuota < raw.TotalQuota || cell.RequestCount < raw.RequestCount {
				return nil, coverage, NewOrganizationInvoiceDataError(errors.New("task/source partition mismatch"))
			}
			cell.TotalQuota -= raw.TotalQuota
			cell.RequestCount -= raw.RequestCount
			quota, rows := &coverage.TaskQuota, &coverage.TaskRows
			if e.unknown {
				quota, rows = &coverage.UnknownQuota, &coverage.UnknownRows
			}
			if err := addOrganizationInvoiceQuota(quota, raw.TotalQuota); err != nil {
				return nil, coverage, err
			}
			if err := addOrganizationInvoiceQuota(rows, raw.RequestCount); err != nil {
				return nil, coverage, err
			}
		}
	}
	// If a known task has no correlated original charge, an unmarked log in
	// its account/model cell is not proven ordinary consumption. Keep that raw
	// bucket unknown rather than billing it alongside the confirmed task again.
	ambiguousCells := make(map[organizationInvoiceCellKey]bool)
	ambiguousUsers := make(map[int]bool)
	for _, e := range evidence {
		if e.firstConsume != nil || !belongs(e.userId, e.origin) {
			continue
		}
		if e.model == "" {
			ambiguousUsers[e.userId] = true
			continue
		}
		ambiguousCells[organizationInvoiceCellKey{userId: e.userId, modelName: e.model, periodMonth: monthKey}] = true
	}
	for key, cell := range cells {
		ambiguous := ambiguousUsers[key.userId] || ambiguousCells[key]
		if !ambiguous {
			continue
		}
		if err := addOrganizationInvoiceQuota(&coverage.UnknownRows, cell.RequestCount); err != nil {
			return nil, coverage, err
		}
		if err := addOrganizationInvoiceQuota(&coverage.UnknownQuota, cell.TotalQuota); err != nil {
			return nil, coverage, err
		}
		cell.RequestCount = 0
		cell.TotalQuota = 0
	}
	for _, e := range evidence {
		if !belongs(e.userId, e.origin) {
			continue
		}
		hasCharge := e.firstConsume != nil
		if !hasCharge {
			coverage.MissingTaskLogs++
		}
		walletReason := ""
		if e.fundingPending {
			walletReason = "task_funding_pending"
		} else if !hasCharge && (len(e.logs) > 0 || e.currentQuota > 0) {
			walletReason = "original_task_charge_missing"
		} else if e.walletMismatch {
			walletReason = "task_cash_flow_mismatch"
		}
		if walletReason != "" && walletReason != "task_funding_pending" && !slices.Contains(coverage.WalletIncompleteUserIds, e.userId) {
			coverage.WalletIncompleteUserIds = append(coverage.WalletIncompleteUserIds, e.userId)
		}
		if walletReason != "" && len(coverage.WalletIssues) < 100 {
			coverage.WalletIssues = append(coverage.WalletIssues, OrganizationInvoiceExpenseIssue{UserId: e.userId, TaskId: e.taskId, Reason: walletReason, OriginAt: e.origin})
		}
		if e.finalQuota == nil || e.pending || e.model == "" || !e.originKnown {
			coverage.PendingTasks++
			reason := "final_expense_evidence_missing"
			if !e.originKnown {
				reason = "expense_origin_missing"
			}
			if len(coverage.Issues) < 100 {
				coverage.Issues = append(coverage.Issues, OrganizationInvoiceExpenseIssue{UserId: e.userId, TaskId: e.taskId, Reason: reason, ReservedQuota: e.currentQuota})
			}
			continue
		}
		key := organizationInvoiceCellKey{userId: e.userId, modelName: e.model, periodMonth: monthKey}
		target := cells[key]
		if target == nil {
			target = &organizationInvoiceAggregate{UserId: e.userId, ModelName: e.model, PeriodMonth: monthKey}
			cells[key] = target
		}
		if err := addOrganizationInvoiceQuota(&target.TotalQuota, *e.finalQuota); err != nil {
			return nil, coverage, err
		}
		target.RequestCount++
	}
	coverage.OrdinaryRows = coverage.TotalRows - coverage.TaskRows - coverage.UnknownRows
	coverage.OrdinaryQuota = coverage.TotalQuota - coverage.TaskQuota - coverage.UnknownQuota
	if coverage.OrdinaryRows < 0 || coverage.OrdinaryQuota < 0 {
		return nil, coverage, NewOrganizationInvoiceDataError(errors.New("invoice source partition conservation failed"))
	}
	if coverage.PendingTasks > 0 || coverage.UnknownRows > 0 || coverage.PendingSourceRows > 0 || !common.LogConsumeEnabled {
		coverage.Status = "incomplete"
	}
	if hasSource {
		source.coverage[slice] = coverage
	}
	rows := make([]organizationInvoiceAggregate, 0, len(cells))
	for _, a := range cells {
		if a.RequestCount > 0 || a.TotalQuota > 0 {
			rows = append(rows, *a)
		}
	}
	return rows, coverage, nil
}

func mergeOrganizationInvoiceExpenseCoverage(target *OrganizationInvoiceExpenseCoverage, value OrganizationInvoiceExpenseCoverage) error {
	if target.Status == "" {
		target.Status = "complete"
	}
	if value.Status != "complete" {
		target.Status = "incomplete"
	}
	fields := []struct {
		p *int64
		v int64
	}{{&target.PendingSourceRows, value.PendingSourceRows}, {&target.TotalRows, value.TotalRows}, {&target.TotalQuota, value.TotalQuota}, {&target.OrdinaryRows, value.OrdinaryRows}, {&target.OrdinaryQuota, value.OrdinaryQuota}, {&target.TaskRows, value.TaskRows}, {&target.TaskQuota, value.TaskQuota}, {&target.UnknownRows, value.UnknownRows}, {&target.UnknownQuota, value.UnknownQuota}, {&target.PendingTasks, value.PendingTasks}, {&target.MissingTaskLogs, value.MissingTaskLogs}}
	for _, f := range fields {
		if err := addOrganizationInvoiceQuota(f.p, f.v); err != nil {
			return err
		}
	}
	for _, id := range value.WalletIncompleteUserIds {
		if !slices.Contains(target.WalletIncompleteUserIds, id) {
			target.WalletIncompleteUserIds = append(target.WalletIncompleteUserIds, id)
		}
	}
	for _, i := range value.WalletIssues {
		if len(target.WalletIssues) < 100 {
			target.WalletIssues = append(target.WalletIssues, i)
		}
	}
	for _, i := range value.Issues {
		if len(target.Issues) < 100 {
			target.Issues = append(target.Issues, i)
		}
	}
	return nil
}

func organizationInvoiceBuildExpenseCoverage(ctx context.Context, summary OrganizationInvoicePeriodSummary, period OrganizationInvoicePeriod) (OrganizationInvoiceExpenseCoverage, error) {
	result := OrganizationInvoiceExpenseCoverage{Status: "complete"}
	shards, err := LoadOrganizationInvoiceUsageShards(summary)
	if err != nil {
		return result, err
	}
	covered, err := organizationInvoiceCoveredShards(organizationInvoiceDaySlices(organizationInvoiceEffectiveDataPeriod(summary, period)), shards)
	if err != nil {
		return result, err
	}
	for _, s := range covered {
		var c OrganizationInvoiceExpenseCoverage
		if len(s.Coverage) > 0 {
			if err := common.Unmarshal(s.Coverage, &c); err != nil {
				return result, err
			}
		}
		if source, ok := ctx.Value(organizationInvoiceSourceContextKey{}).(*organizationInvoiceSource); ok {
			day := organizationInvoiceEvidenceDay(s.ShardStart)
			if source.walletIncompleteDays == nil {
				source.walletIncompleteDays = make(map[int64]map[int]bool)
			}
			if source.walletIncompleteDays[day] == nil {
				source.walletIncompleteDays[day] = make(map[int]bool)
			}
			for _, id := range c.WalletIncompleteUserIds {
				source.walletIncompleteDays[day][id] = true
			}
		}
		if err := mergeOrganizationInvoiceExpenseCoverage(&result, c); err != nil {
			return result, err
		}
	}
	return result, nil
}

func collectOrganizationInvoiceTaskEvent(e *organizationInvoiceTaskEvidence, r organizationInvoiceExpenseLog, m organizationInvoiceTaskLogMetadata, included bool) error {
	if r.Type == LogTypeConsume && included {
		if e.consumes == nil {
			e.consumes = make(map[string]organizationInvoiceAggregate)
		}
		cell := e.consumes[r.ModelName]
		if err := addOrganizationInvoiceQuota(&cell.TotalQuota, r.Quota); err != nil {
			return err
		}
		if err := addOrganizationInvoiceQuota(&cell.RequestCount, 1); err != nil {
			return err
		}
		e.consumes[r.ModelName] = cell
	}
	if r.Type == LogTypeConsume && m.ActualQuota == nil && m.PreQuota == nil && (e.firstConsume == nil || r.CreatedAt < e.firstConsume.CreatedAt) {
		copy := r
		e.firstConsume = &copy
		e.originKnown = true
		e.origin = r.CreatedAt
		e.model = r.ModelName
	}
	if len(e.logs) < organizationInvoiceTaskEventReferenceLimit {
		e.logs = append(e.logs, r)
	} else {
		e.truncated = true
		e.walletMismatch = true
	}
	return nil
}

// Consumption ownership follows the existing member interval, even when a
// task is completed or refunded after that member has left the organization.
func organizationInvoiceSliceOwnsConsumption(members []OrganizationMember, scope organizationInvoiceTimeSlice, userId int, at int64) bool {
	for _, member := range members {
		if member.UserId != userId {
			continue
		}
		lo, hi, ok := organizationInvoiceMembershipSliceBounds(member, scope)
		if ok && at >= lo && at < hi {
			return true
		}
	}
	return false
}
