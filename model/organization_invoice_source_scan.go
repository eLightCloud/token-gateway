package model

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const organizationInvoiceTaskAssociationScope = "task-source-associations"

// prepareOrganizationInvoiceLogEvidence classifies only task candidates once.
// ID pages bound physical candidate ranges as well as result sizes: a sparse
// task predicate must not turn LIMIT into an unbounded scan of millions of
// ordinary other documents. Checkpoints and normalized associations commit
// together. No ordinary consumption is copied, and no money fact is created.
func prepareOrganizationInvoiceLogEvidence(ctx context.Context, source *organizationInvoiceSource, org int, userIds []int) error {
	seenUsers := make(map[int]bool, len(userIds))
	for _, userId := range userIds {
		if seenUsers[userId] {
			continue
		}
		seenUsers[userId] = true
		if err := prepareOrganizationInvoiceMemberLogEvidence(ctx, source, org, userId); err != nil {
			return err
		}
	}
	return nil
}

func prepareOrganizationInvoiceMemberLogEvidence(ctx context.Context, source *organizationInvoiceSource, org, userId int) error {
	var cursor OrganizationInvoiceSourceCursor
	err := DB.WithContext(ctx).Where("organization_id = ? AND user_id = ?", org, userId).First(&cursor).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	if cursor.LogId > source.headLogId {
		if source.clearedBefore == 0 {
			return NewOrganizationInvoiceDataError(errors.New("log source was replaced; explicitly reset its invoice cache cursor"))
		}
		if err := DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Where("organization_id = ? AND scope_hash LIKE ?", org, fmt.Sprintf("%s:%d:%%", organizationInvoiceTaskAssociationScope, userId)).Delete(&OrganizationInvoiceDayCache{}).Error; err != nil {
				return err
			}
			return tx.Model(&OrganizationInvoiceSourceCursor{}).Where("organization_id = ? AND user_id = ?", org, userId).Updates(map[string]any{"prefix_until": 0, "prefix_head": 0, "prefix_log_id": 0, "log_id": 0, "revision": gorm.Expr("revision + 1")}).Error
		}); err != nil {
			return err
		}
	}
	if err := DB.WithContext(ctx).Model(&OrganizationInvoiceSourceCursor{}).Where("organization_id = ? AND user_id = ?", org, userId).Update("read_head", gorm.Expr("CASE WHEN read_head < ? THEN ? ELSE read_head END", source.logId, source.logId)).Error; err != nil {
		return err
	}
	if err := DB.WithContext(ctx).Where("organization_id = ? AND user_id = ?", org, userId).First(&cursor).Error; err != nil {
		return err
	}
	readUntil := source.logId
	if cursor.PrefixUntil > 0 {
		cursor.prefix = true
		cursor.LogId = cursor.PrefixLogId
		readUntil = cursor.PrefixHead
	}
	for cursor.LogId < readUntil {
		if err := organizationStatsCheckBoundary(ctx); err != nil {
			return err
		}
		var headers []Log
		err := withOrganizationInvoiceHeavyLogQuery(ctx, func(tx *gorm.DB) error {
			query := tx.Model(&Log{}).Select("id,user_id,created_at,type").Where("user_id = ? AND id > ? AND id <= ?", userId, cursor.LogId, readUntil)
			if cursor.prefix {
				query = query.Where("created_at >= ? AND created_at < ?", cursor.FromAt, cursor.PrefixUntil)
			}
			return query.Order("id").Limit(organizationStatsReadBatchSize(ctx)).Find(&headers).Error
		})
		if err != nil {
			return err
		}
		if len(headers) == 0 {
			err := advanceOrganizationInvoiceSourceCursor(DB.WithContext(ctx), cursor, readUntil)
			if err == nil {
				recordOrganizationStatsProgress(ctx)
				if cursor.prefix {
					return finishOrganizationInvoiceSourcePrefix(ctx, source, cursor)
				}
			}
			return err
		}
		if source.metrics != nil {
			source.metrics.SourceHeaderRows += int64(len(headers))
		}
		last := int64(headers[len(headers)-1].Id)
		var candidates []organizationInvoiceExpenseLog
		if userId > 0 {
			err = withOrganizationInvoiceHeavyLogQuery(ctx, func(tx *gorm.DB) error {
				query := tx.Model(&Log{}).Select("id,user_id,created_at,type,quota,model_name,other").Where("id > ? AND id <= ? AND user_id = ? AND type IN ?", cursor.LogId, last, userId, []int{LogTypeConsume, LogTypeRefund}).Where("created_at >= ?", cursor.FromAt).Where("other LIKE ? OR other LIKE ? OR model_name LIKE ? OR model_name = ?", `%"task_id"%`, `%"is_task"%`, "mj_%", "swap_face")
				if cursor.prefix {
					query = query.Where("created_at < ?", cursor.PrefixUntil)
				}
				return query.Order("id").Find(&candidates).Error
			})
			if err != nil {
				return err
			}
		}
		if source.metrics != nil {
			source.metrics.SourceTaskRows += int64(len(candidates))
			for _, row := range candidates {
				source.metrics.SourceOtherBytes += int64(len(row.Other))
			}
		}
		for i := range candidates {
			var metadata organizationInvoiceTaskLogMetadata
			if common.UnmarshalJsonStr(candidates[i].Other, &metadata) != nil {
				metadata = organizationInvoiceTaskLogMetadata{IsTask: true}
			}
			b, err := common.Marshal(metadata)
			if err != nil {
				return err
			}
			candidates[i].Other = string(b)
		}
		affected := make(map[int64]int64)
		var firstChangedAt int64
		for _, h := range headers {
			if h.UserId == userId && (h.Type == LogTypeConsume || h.Type == LogTypeRefund || h.Type == LogTypeManage || h.Type == LogTypeTopup) {
				if firstChangedAt == 0 || h.CreatedAt < firstChangedAt {
					firstChangedAt = h.CreatedAt
				}
				d := organizationInvoiceEvidenceDay(h.CreatedAt)
				affected[d] = max(affected[d], int64(h.Id))
			}
		}
		// A newly arrived task adjustment changes its original expense day,
		// even if the cash adjustment itself belongs to a later month.
		keys := make([]string, 0, len(candidates))
		for _, r := range candidates {
			var m organizationInvoiceTaskLogMetadata
			if common.UnmarshalJsonStr(r.Other, &m) == nil && m.TaskId != "" {
				keys = append(keys, m.TaskId)
			}
		}
		for start := 0; start < len(keys); start += 128 {
			end := min(start+128, len(keys))
			var tasks []Task
			if err := DB.WithContext(ctx).Select("task_id,created_at,invoice_origin_at").Where("user_id = ? AND task_id IN ?", userId, keys[start:end]).Find(&tasks).Error; err != nil {
				return err
			}
			origins := make(map[string]int64, len(tasks))
			for _, t := range tasks {
				origin := t.CreatedAt
				if t.InvoiceOriginAt > 0 {
					origin = t.InvoiceOriginAt
				}
				origins[t.TaskID] = origin
			}
			var mj []Midjourney
			if err := DB.WithContext(ctx).Select("mj_id,submit_time,invoice_origin_at").Where("user_id = ? AND mj_id IN ?", userId, keys[start:end]).Find(&mj).Error; err != nil {
				return err
			}
			for _, t := range mj {
				origin := t.SubmitTime / 1000
				if t.InvoiceOriginAt > 0 {
					origin = t.InvoiceOriginAt
				}
				origins[t.MjId] = origin
			}
			for _, r := range candidates {
				var m organizationInvoiceTaskLogMetadata
				if common.UnmarshalJsonStr(r.Other, &m) != nil {
					continue
				}
				if origin := origins[m.TaskId]; origin > 0 {
					if firstChangedAt == 0 || origin < firstChangedAt {
						firstChangedAt = origin
					}
					day := organizationInvoiceEvidenceDay(origin)
					affected[day] = max(affected[day], int64(r.Id))
				}
			}
		}

		err = DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if source.owner != nil {
				var current OrganizationInvoicePeriodSummary
				// Serialize this checkpoint with owner invalidation. A delivery must
				// not be followed by an older page publishing over its repair;
				// the cursor revision also guards checkpoints without an owner.
				err := lockForUpdate(tx).Select("id").Where("id = ? AND revision = ? AND status = ?", source.owner.Id, source.owner.Revision, OrganizationInvoiceSummaryStatusBuilding).First(&current).Error
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return ErrOrganizationInvoiceBuildSuperseded
				}
				if err != nil {
					return err
				}
			}
			var earliest, maxChanged int64
			for day, id := range affected {
				if earliest == 0 || day < earliest {
					earliest = day
				}
				maxChanged = max(maxChanged, id)
			}
			// Lock/update summaries before cache rows, matching invalidation
			// and publication; never hold a day row while waiting on a bill.
			if earliest > 0 {
				if err := tx.Model(&OrganizationInvoicePeriodSummary{}).Where("organization_id = ? AND status = ? AND period_end >= ?", org, OrganizationInvoiceSummaryStatusReady, earliest).Where("source_as_of >= ? OR source_as_of = 0", firstChangedAt).Where("log_high_watermark IS NULL OR log_high_watermark < ?", maxChanged).Updates(map[string]any{"status": OrganizationInvoiceSummaryStatusInvalidated, "payload": nil, "wallet_payload": nil, "rule_recalc_payload": nil, "error": "late source evidence", "updated_at": common.GetTimestamp()}).Error; err != nil {
					return err
				}
			}
			for day, changedId := range affected {
				if err := tx.Model(&OrganizationInvoiceDayCache{}).Where("organization_id = ? AND start_at < ? AND end_at > ? AND scope_hash NOT LIKE ?", org, day+86400, day, organizationInvoiceTaskAssociationScope+":%").Where("log_id < ?", changedId).Updates(map[string]any{"usage": nil, "coverage": nil, "wallet_log_facts": nil, "wallet": nil}).Error; err != nil {
					return err
				}
			}

			if len(candidates) > 0 {
				lo, hi := candidates[0].CreatedAt, candidates[0].CreatedAt
				for _, r := range candidates {
					lo = min(lo, r.CreatedAt)
					hi = max(hi, r.CreatedAt)
				}
				b, err := common.Marshal(candidates)
				if err != nil {
					return err
				}
				scopeHash := fmt.Sprintf("%s:%d:%d", organizationInvoiceTaskAssociationScope, userId, cursor.LogId)
				if cursor.prefix {
					scopeHash = fmt.Sprintf("%s:%d:prefix:%d:%d", organizationInvoiceTaskAssociationScope, userId, cursor.PrefixUntil, cursor.LogId)
				}
				row := OrganizationInvoiceDayCache{SourceStartId: cursor.LogId, LogId: last, OrganizationId: org, StartAt: lo, EndAt: hi + 1, ScopeHash: scopeHash, Version: OrganizationInvoiceSummaryCalculationVersion, TaskEvidence: b}
				if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "organization_id"}, {Name: "start_at"}, {Name: "end_at"}, {Name: "scope_hash"}, {Name: "version"}}, DoUpdates: clause.Assignments(map[string]any{"task_evidence": b, "source_start_id": cursor.LogId, "log_id": last, "updated_at": common.GetTimestamp()})}).Create(&row).Error; err != nil {
					return err
				}
			}
			return advanceOrganizationInvoiceSourceCursor(tx, cursor, last)
		})
		if err != nil {
			return err
		}
		recordOrganizationStatsProgress(ctx)
		cursor.LogId = last
		if source.owner != nil {
			if err := TouchOrganizationInvoiceBuildStage(*source.owner, OrganizationInvoiceStageSource, common.GetTimestamp()); err != nil {
				return err
			}
		}
	}
	if cursor.prefix {
		return finishOrganizationInvoiceSourcePrefix(ctx, source, cursor)
	}
	return nil
}

func organizationInvoiceEvidenceDay(at int64) int64 {
	d := time.Unix(at, 0).In(organizationInvoiceLocation)
	return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, organizationInvoiceLocation).Unix()
}

func forEachOrganizationInvoiceTaskAssociation(ctx context.Context, org int, period OrganizationInvoicePeriod, evidenceAt int64, apply func([]organizationInvoiceExpenseLog) error) error {
	if source, ok := ctx.Value(organizationInvoiceSourceContextKey{}).(*organizationInvoiceSource); ok && !common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		var after int64
		for {
			var rows []OrganizationInvoiceDayCache
			if err := DB.WithContext(ctx).Select("id,task_evidence").Where("organization_id = ? AND scope_hash LIKE ? AND version = ? AND end_at > ? AND start_at <= ? AND id > ?", org, organizationInvoiceTaskAssociationScope+":%", OrganizationInvoiceSummaryCalculationVersion, period.StartTimestamp, evidenceAt, after).Order("id").Limit(1).Find(&rows).Error; err != nil {
				return err
			}
			if len(rows) == 0 {
				break
			}
			var associations []organizationInvoiceExpenseLog
			if err := common.Unmarshal(rows[0].TaskEvidence, &associations); err != nil {
				return err
			}
			kept := associations[:0]
			for _, a := range associations {
				if int64(a.Id) <= source.logId && a.CreatedAt >= source.clearedBefore && a.CreatedAt >= period.StartTimestamp && a.CreatedAt <= evidenceAt {
					kept = append(kept, a)
				}
			}
			if err := apply(kept); err != nil {
				return err
			}
			after = rows[0].Id
		}
		return nil
	}
	ids, err := GetOrganizationInvoiceAccountUserIds(org, period)
	if err != nil || len(ids) == 0 {
		return err
	}
	return forEachOrganizationInvoiceKeysetBatch[organizationInvoiceExpenseLog](ctx, []string{"id", "user_id", "created_at", "type", "quota", "model_name", "other"}, func(q *gorm.DB) *gorm.DB {
		return q.Where("user_id IN ? AND created_at >= ? AND created_at <= ? AND type IN ?", ids, period.StartTimestamp, evidenceAt, []int{LogTypeConsume, LogTypeRefund}).Where("other LIKE ? OR other LIKE ? OR model_name LIKE ? OR model_name = ?", `%"task_id"%`, `%"is_task"%`, "mj_%", "swap_face")
	}, apply)
}
