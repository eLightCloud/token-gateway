package model

import (
	"context"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

type organizationInvoicePendingSettlement struct {
	TaskSettlementJournal
	Origin         int64
	LogType        int
	PayloadUnknown bool
}

// Temporary outboxes are coverage checks, never a second charge source. A
// missing final log cannot be silently treated as zero/complete. Read pending
// operations in finite pages and retain only bounded diagnostic references.
func forEachOrganizationInvoicePendingSettlement(ctx context.Context, db *gorm.DB, userIds []int, apply func(organizationInvoicePendingSettlement) error) error {
	if len(userIds) == 0 {
		return nil
	}
	var after int64
	for {
		var rows []TaskSettlementJournal
		err := db.WithContext(ctx).Select("id,request_id,user_id,created_at,operation,entity_type,invoice_task_entity,billing_source,log_payload").Where("id > ? AND user_id IN ? AND (applied = ? OR log_done = ?)", after, userIds, false, false).Order("id").Limit(organizationInvoiceReadBatchSize()).Find(&rows).Error
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return nil
		}
		for _, j := range rows {
			pending := organizationInvoicePendingSettlement{TaskSettlementJournal: j, Origin: j.CreatedAt}
			if j.LogPayload != "" {
				var params RecordTaskBillingLogParams
				if err := common.UnmarshalJsonStr(j.LogPayload, &params); err != nil {
					pending.PayloadUnknown = true
					pending.Origin = 0
				} else if params.CreatedAt > 0 {
					pending.Origin = params.CreatedAt
					pending.LogType = params.LogType
				}
			}
			if err := apply(pending); err != nil {
				return err
			}
		}
		after = rows[len(rows)-1].Id
		if len(rows) < organizationInvoiceReadBatchSize() {
			return nil
		}
	}
}

func organizationInvoicePendingExpenseEvidence(ctx context.Context, org int, period OrganizationInvoicePeriod) (int64, []OrganizationInvoiceExpenseIssue, error) {
	ids, err := GetOrganizationInvoiceAccountUserIds(org, period)
	if err != nil {
		return 0, nil, err
	}
	members, err := activeAndHistoricalOrganizationMembers(org, 0)
	if err != nil {
		return 0, nil, err
	}
	var count int64
	var issues []OrganizationInvoiceExpenseIssue
	err = forEachOrganizationInvoicePendingSettlement(ctx, DB, ids, func(p organizationInvoicePendingSettlement) error {
		if p.Operation != TaskSettlementOperationSettle || p.EntityType != "" || p.InvoiceTaskEntity != "" || (p.LogType != LogTypeConsume && !p.PayloadUnknown) {
			return nil
		}
		if !p.PayloadUnknown {
			found := false
			for _, m := range members {
				lo, hi, exclusive, ok := logMembershipBounds(m, OrganizationBillingFilters{StartTimestamp: period.StartTimestamp, EndTimestamp: period.EndTimestamp})
				if m.UserId == p.UserId && ok && p.Origin >= lo && ((exclusive && p.Origin < hi) || (!exclusive && p.Origin <= hi)) {
					found = true
					break
				}
			}
			if !found {
				return nil
			}
		}
		if err := addOrganizationInvoiceQuota(&count, 1); err != nil {
			return err
		}
		if len(issues) < 100 {
			issues = append(issues, OrganizationInvoiceExpenseIssue{UserId: p.UserId, TaskId: p.RequestId, Reason: "consume_log_pending"})
		}
		return nil
	})
	return count, issues, err
}

// InvalidateOrganizationInvoicePendingLog runs after the durable intent commits,
// including recovery attempts. A fresh read sees concurrently registered builds;
// it must not reuse a funding transaction's older MySQL repeatable-read snapshot.
func InvalidateOrganizationInvoicePendingLog(j *TaskSettlementJournal) error {
	if j.LogDone || j.LogPayload == "" {
		return nil
	}
	var params RecordTaskBillingLogParams
	if err := common.UnmarshalJsonStr(j.LogPayload, &params); err != nil {
		return err
	}
	return acknowledgeOrganizationInvoiceLog(&Log{UserId: params.UserId, CreatedAt: params.CreatedAt, Type: params.LogType})
}
