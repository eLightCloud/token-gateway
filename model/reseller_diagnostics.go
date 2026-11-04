package model

import (
	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type ResellerRequestDiagnostic struct {
	ID             string `json:"request_id"`
	CustomerUserID int    `json:"customer_id"`
	State          string `json:"state"`
	Version        int64  `json:"version"`
	Model          string `json:"model"`
	ChannelID      int    `json:"channel_id"`
	BaseQuota      int64  `json:"base_quota"`
	CustomerQuota  *int64 `json:"customer_quota"`
	AgentQuota     *int64 `json:"agent_quota"`
	ReservedQuota  int64  `json:"reserved_quota"`
	OccurredAt     int64  `json:"occurred_at"`
}
type ResellerDiagnostics struct {
	Account           ResellerAccount             `json:"account"`
	PendingEvents     int64                       `json:"pending_events"`
	OldestPendingAt   int64                       `json:"oldest_pending_at"`
	BalanceFromFacts  int64                       `json:"balance_from_facts"`
	ReservedFromFacts int64                       `json:"reserved_from_facts"`
	Consistent        bool                        `json:"consistent"`
	Requests          []ResellerRequestDiagnostic `json:"requests"`
}

// ReadResellerDiagnostics never exposes request bodies, keys, or internal
// snapshots. The same account lock used by publication gives a coherent view.
func ReadResellerDiagnostics(operatorID, agentID int) (*ResellerDiagnostics, error) {
	result := &ResellerDiagnostics{}
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := ResellerManageTarget(tx, operatorID, agentID, true); err != nil {
			return err
		}
		if err := lockForUpdate(tx).First(&result.Account, "user_id = ?", agentID).Error; err != nil {
			return err
		}
		if err := tx.Model(&ResellerEvent{}).Where("agent_user_id = ? AND processed_at = ?", agentID, 0).Count(&result.PendingEvents).Error; err != nil {
			return err
		}
		if err := tx.Model(&ResellerEvent{}).Select("COALESCE(MIN(occurred_at), 0)").Where("agent_user_id = ? AND processed_at = ?", agentID, 0).Scan(&result.OldestPendingAt).Error; err != nil {
			return err
		}
		if err := tx.Model(&ResellerQuotaAdjustment{}).Select("COALESCE(SUM(CASE WHEN mode = 'add' THEN quota ELSE -quota END), 0)").Where("user_id = ? AND account_kind = ?", agentID, "agent").Scan(&result.BalanceFromFacts).Error; err != nil {
			return err
		}
		var sums struct {
			Spend   int64
			Reserve int64
		}
		if err := tx.Model(&ResellerEvent{}).Select("COALESCE(SUM(spend_delta), 0) AS spend, COALESCE(SUM(reserve_delta), 0) AS reserve").Where("agent_user_id = ? AND processed_at > ?", agentID, 0).Scan(&sums).Error; err != nil {
			return err
		}
		result.BalanceFromFacts -= sums.Spend
		result.ReservedFromFacts = sums.Reserve
		result.Consistent = result.Account.Balance == result.BalanceFromFacts && result.Account.Reserved == sums.Reserve
		return tx.Model(&ResellerRequest{}).Where("agent_user_id = ?", agentID).Order("occurred_at DESC, id").Limit(100).Find(&result.Requests).Error
	})
	return result, err
}

// RebuildResellerAccountProjection is an explicit repair using immutable
// funding adjustments and already-published events. Pending events remain
// pending and are consumed normally; it never invents an upstream outcome.
func RebuildResellerAccountProjection(operatorID, agentID int) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		if err := ResellerManageTarget(tx, operatorID, agentID, true); err != nil {
			return err
		}
		var account ResellerAccount
		if err := lockForUpdate(tx).First(&account, "user_id = ?", agentID).Error; err != nil {
			return err
		}
		var funding int64
		if err := tx.Model(&ResellerQuotaAdjustment{}).Select("COALESCE(SUM(CASE WHEN mode = 'add' THEN quota ELSE -quota END), 0)").Where("user_id = ? AND account_kind = ?", agentID, "agent").Scan(&funding).Error; err != nil {
			return err
		}
		var sums struct {
			Spend   int64
			Reserve int64
		}
		if err := tx.Model(&ResellerEvent{}).Select("COALESCE(SUM(spend_delta), 0) AS spend, COALESCE(SUM(reserve_delta), 0) AS reserve").Where("agent_user_id = ? AND processed_at > ?", agentID, 0).Scan(&sums).Error; err != nil {
			return err
		}
		if sums.Reserve < 0 {
			return ErrResellerConflict
		}
		cursor := ""
		for {
			var projections []ResellerProjection
			if err := tx.Model(&ResellerEvent{}).Select("request_id, MAX(version) AS version").Where("agent_user_id = ? AND processed_at > ? AND request_id > ?", agentID, 0, cursor).Group("request_id").Order("request_id").Limit(500).Scan(&projections).Error; err != nil {
				return err
			}
			for _, projection := range projections {
				if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "request_id"}}, DoUpdates: clause.AssignmentColumns([]string{"version"})}).Create(&projection).Error; err != nil {
					return err
				}
				cursor = projection.RequestID
			}
			if len(projections) < 500 {
				break
			}
		}
		return tx.Model(&account).Updates(map[string]any{"balance": funding - sums.Spend, "reserved": sums.Reserve, "revision": account.Revision + 1, "published_at": common.GetTimestamp()}).Error
	})
}

type ResellerPublicationLag struct {
	PendingEvents   int64 `json:"pending_events"`
	OldestPendingAt int64 `json:"oldest_pending_at"`
}

func ReadResellerPublicationLag() (ResellerPublicationLag, error) {
	var lag ResellerPublicationLag
	err := DB.Model(&ResellerEvent{}).Select("COUNT(*) AS pending_events, COALESCE(MIN(occurred_at), 0) AS oldest_pending_at").Where("processed_at = ?", 0).Scan(&lag).Error
	return lag, err
}
