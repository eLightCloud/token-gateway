package model

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"math"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/types"
	"gorm.io/gorm"
)

// Receipts survive journal cleanup and are checked before any wallet mutation.
type ResellerBillingReceipt struct {
	OperationID string `gorm:"size:64;primaryKey"`
	Fact        string `gorm:"type:text;not null"`
	OccurredAt  int64
}
type ResellerProjection struct {
	RequestID string `gorm:"size:64;primaryKey"`
	Version   int64
}

func PersonalBillingReplayTx(tx *gorm.DB, fact *types.PersonalBillingFact) (bool, error) {
	if fact == nil {
		return false, nil
	}
	encoded, err := common.Marshal(fact)
	if err != nil {
		return false, err
	}
	var receipt ResellerBillingReceipt
	err = tx.First(&receipt, "operation_id = ?", fact.OperationID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if receipt.Fact != string(encoded) {
		return false, ErrResellerConflict
	}
	return true, nil
}

func applyPersonalBillingFactTx(tx *gorm.DB, fact *types.PersonalBillingFact) error {
	if fact == nil {
		return nil
	}
	if fact.RequestID == "" || len(fact.RequestID) > 64 || fact.OperationID == "" || len(fact.OperationID) > 64 || fact.UserID <= 0 || fact.BaseQuota < 0 || fact.CustomerTarget < 0 || fact.AgentTarget < 0 || fact.BaseQuota > int64(common.MaxQuota) || fact.CustomerTarget > int64(common.MaxQuota) || fact.AgentTarget > int64(common.MaxQuota) {
		return ErrResellerInvalid
	}
	replay, err := PersonalBillingReplayTx(tx, fact)
	if err != nil || replay {
		return err
	}
	var state ResellerRequest
	err = lockForUpdate(tx).First(&state, "id = ?", fact.RequestID).Error
	fresh := errors.Is(err, gorm.ErrRecordNotFound)
	if err != nil && !fresh {
		return err
	}
	if fresh {
		state = ResellerRequest{ID: fact.RequestID, CustomerUserID: fact.UserID, AgentUserID: fact.AgentUserID, CreatedAt: common.GetTimestamp()}
	}
	if fact.ExpectedState != "" && state.State != fact.ExpectedState {
		return ErrResellerConflict
	}
	if state.CustomerUserID != fact.UserID || state.AgentUserID != fact.AgentUserID {
		return ErrResellerConflict
	}
	if state.State == "refunded" || (state.State == "settled" && fact.Kind == "reserve") {
		return ErrResellerConflict
	}
	var oldCustomer int64
	if state.CustomerQuota != nil {
		oldCustomer = *state.CustomerQuota
	}
	var oldSpend int64
	if state.AgentQuota != nil {
		oldSpend = *state.AgentQuota
	}
	oldReserve := state.ReservedQuota
	switch fact.Kind {
	case "reserve", "pending":
		state.ReservedQuota = fact.AgentTarget
		if state.State != "outcome_unknown" {
			state.State = "reserved"
		}
		if fact.Kind == "pending" {
			state.State = "pending"
		}
	case "settle":
		state.ReservedQuota = 0
		state.State = "settled"
		if state.OriginalOperationID == "" {
			state.OriginalOperationID = fact.OperationID
		}
		customer, agent := fact.CustomerTarget, fact.AgentTarget
		state.CustomerQuota = &customer
		state.AgentQuota = &agent
	case "refund":
		// Refunds supply the remaining cumulative target from the original snapshot.
		if state.CustomerQuota != nil && (fact.CustomerTarget > *state.CustomerQuota || fact.AgentTarget > oldSpend || fact.BaseQuota > state.BaseQuota) {
			return ErrResellerInvalid
		}
		state.ReservedQuota = 0
		customer, agent := fact.CustomerTarget, fact.AgentTarget
		state.CustomerQuota = &customer
		state.AgentQuota = &agent
		state.State = "partially_refunded"
		if fact.CustomerTarget == 0 && fact.AgentTarget == 0 {
			state.State = "refunded"
		}
	default:
		return ErrResellerInvalid
	}
	encoded, err := common.Marshal(fact)
	if err != nil {
		return err
	}
	if fact.EntityID > 0 {
		state.EntityType, state.EntityID = fact.EntityType, fact.EntityID
		state.TaskID = fmt.Sprintf("%s:%d", fact.EntityType, fact.EntityID)
	}
	state.Model = fact.Model
	state.ChannelID = fact.ChannelID
	state.Snapshot = string(encoded)
	state.BaseQuota = fact.BaseQuota
	state.Version++
	state.OccurredAt = common.GetTimestamp()
	if fresh {
		err = tx.Create(&state).Error
	} else {
		err = tx.Save(&state).Error
	}
	if err != nil {
		return err
	}
	if state.AgentUserID > 0 {
		spend := int64(0)
		if state.AgentQuota != nil {
			spend = *state.AgentQuota
		}
		kind := fact.Kind
		if kind == "pending" {
			kind = "reserve"
		}
		customerDelta := int64(0)
		if state.CustomerQuota != nil {
			customerDelta = *state.CustomerQuota - oldCustomer
		}
		event := ResellerEvent{CustomerDelta: customerDelta, OperationID: fact.OperationID, RequestID: state.ID, Version: state.Version, Kind: kind, AgentUserID: state.AgentUserID, CustomerUserID: state.CustomerUserID, Model: state.Model, ChannelID: state.ChannelID, Snapshot: state.Snapshot, BaseQuota: fact.BaseQuota, CustomerTarget: fact.CustomerTarget, AgentTarget: fact.AgentTarget, ReserveDelta: state.ReservedQuota - oldReserve, SpendDelta: spend - oldSpend, OriginalOperationID: state.OriginalOperationID, OccurredAt: state.OccurredAt}
		if err := tx.Create(&event).Error; err != nil {
			return err
		}
	}
	return tx.Create(&ResellerBillingReceipt{OperationID: fact.OperationID, Fact: string(encoded), OccurredAt: state.OccurredAt}).Error
}

func PersonalOperationID(requestID, operation string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(requestID+":"+operation)))
}

// Bind the durable upstream task to its request and settlement intent in the
// insertion transaction, before any poller or recovery worker can see the row.
func bindPersonalTaskJournalTx(tx *gorm.DB, journal *TaskSettlementJournal) error {
	if journal.PersonalFact == "" {
		return nil
	}
	var fact types.PersonalBillingFact
	if err := common.UnmarshalJsonStr(journal.PersonalFact, &fact); err != nil {
		return err
	}
	fact.EntityType, fact.EntityID = journal.EntityType, journal.EntityId
	encoded, err := common.Marshal(fact)
	if err != nil {
		return err
	}
	journal.PersonalFact = string(encoded)
	journal.InvoiceTaskEntity, journal.InvoiceTaskId = journal.EntityType, journal.EntityId
	if fact.Kind == "settle" {
		quota := int64(journal.FinalQuota)
		journal.InvoiceExpenseQuota = &quota
	}
	result := tx.Model(&ResellerRequest{}).
		Where("id = ? AND customer_user_id = ? AND entity_id = ?", fact.RequestID, fact.UserID, 0).
		Updates(map[string]any{"entity_type": fact.EntityType, "entity_id": fact.EntityID, "task_id": fmt.Sprintf("%s:%d", fact.EntityType, fact.EntityID)})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrResellerConflict
	}
	return nil
}

// Validate the journal's funding expectation against the locked money fact,
// not task.quota. Different operations may otherwise refund the same old debit.
func validatePersonalJournalBalanceTx(tx *gorm.DB, journal *TaskSettlementJournal) error {
	if journal.PersonalFact == "" || journal.Operation == TaskSettlementOperationReserve {
		return nil
	}
	var fact types.PersonalBillingFact
	if err := common.UnmarshalJsonStr(journal.PersonalFact, &fact); err != nil {
		return err
	}
	var state ResellerRequest
	err := lockForUpdate(tx).First(&state, "id = ?", fact.RequestID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) && journal.ExpectedQuota == 0 {
		return nil // One-shot charges have no preceding reservation.
	}
	if err != nil {
		return err
	}
	var previous types.PersonalBillingFact
	if err := common.UnmarshalJsonStr(state.Snapshot, &previous); err != nil {
		return err
	}
	if state.CustomerUserID != journal.UserId || previous.CustomerTarget != int64(journal.ExpectedQuota) {
		return ErrResellerConflict
	}
	return nil
}

// ProcessResellerEvents claims a bounded batch with ordinary row locks, valid
// on MySQL 5.7 and PostgreSQL 9.6. No maximum-ID watermark can skip late commits.
// Locking the account serializes concurrent publishers and quota adjustments.
func ProcessResellerEvents(limit int) (int, error) {
	if limit < 1 || limit > 2000 {
		return 0, ErrResellerInvalid
	}
	processed := 0
	err := DB.Transaction(func(tx *gorm.DB) error {
		var first ResellerEvent
		err := tx.Where("processed_at = ?", 0).Order("id").First(&first).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		var account ResellerAccount
		if err := lockForUpdate(tx).First(&account, "user_id = ?", first.AgentUserID).Error; err != nil {
			return err
		}
		var events []ResellerEvent
		if err := lockForUpdate(tx).Where("processed_at = ? AND agent_user_id = ?", 0, account.UserID).Order("id").Limit(limit).Find(&events).Error; err != nil {
			return err
		}
		var reserveDelta, spendDelta int64
		now := common.GetTimestamp()
		for _, event := range events {
			var projection ResellerProjection
			err := tx.First(&projection, "request_id = ?", event.RequestID).Error
			fresh := errors.Is(err, gorm.ErrRecordNotFound)
			if err != nil && !fresh {
				return err
			}
			if event.Version != projection.Version+1 {
				continue
			}
			reserveDelta += event.ReserveDelta
			spendDelta += event.SpendDelta
			projection.RequestID = event.RequestID
			projection.Version = event.Version
			if fresh {
				err = tx.Create(&projection).Error
			} else {
				err = tx.Save(&projection).Error
			}
			if err != nil {
				return err
			}
			if err := tx.Model(&ResellerEvent{}).Where("id = ? AND processed_at = ?", event.ID, 0).Updates(map[string]any{"processed_at": now, "processed_revision": account.Revision + 1}).Error; err != nil {
				return err
			}
			processed++
		}
		if processed == 0 {
			return nil
		}
		// Per-operation charges are int32 bounded; batch size bounds the additions.
		if (spendDelta > 0 && account.Balance < math.MinInt64+spendDelta) || (spendDelta < 0 && account.Balance > math.MaxInt64+spendDelta) || (reserveDelta > 0 && account.Reserved > math.MaxInt64-reserveDelta) || account.Reserved+reserveDelta < 0 {
			return ErrResellerConflict
		}
		return tx.Model(&ResellerAccount{}).Where("user_id = ?", account.UserID).Updates(map[string]any{"balance": gorm.Expr("balance - ?", spendDelta), "reserved": gorm.Expr("reserved + ?", reserveDelta), "revision": gorm.Expr("revision + 1"), "published_at": now}).Error
	})
	return processed, err
}

func restorePersonalJournalReceiptTx(tx *gorm.DB, journal *TaskSettlementJournal) error {
	if journal.PersonalFact == "" {
		return nil
	}
	var fact types.PersonalBillingFact
	if err := common.UnmarshalJsonStr(journal.PersonalFact, &fact); err != nil {
		return err
	}
	replay, err := PersonalBillingReplayTx(tx, &fact)
	if err != nil {
		return err
	}
	if replay {
		journal.Applied = true
		return tx.Model(&TaskSettlementJournal{}).Where("id = ?", journal.Id).Update("applied", true).Error
	}
	return nil
}
func applyPersonalJournalTx(tx *gorm.DB, journal *TaskSettlementJournal) error {
	if journal.PersonalFact == "" {
		return nil
	}
	var fact types.PersonalBillingFact
	if err := common.UnmarshalJsonStr(journal.PersonalFact, &fact); err != nil {
		return err
	}
	if fact.UserID != journal.UserId || fact.CustomerTarget != int64(journal.FinalQuota) {
		return ErrResellerConflict
	}
	var funding ResellerFundingSnapshot
	err := tx.First(&funding, "request_id = ?", fact.RequestID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if err := tx.Create(&ResellerFundingSnapshot{RequestID: fact.RequestID, TokenID: journal.TokenId, ApplyToken: journal.ApplyToken, BillingSource: journal.BillingSource, SubscriptionID: journal.SubscriptionId}).Error; err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	return applyPersonalBillingFactTx(tx, &fact)
}

// RecordPersonalReservation persists a channel change requiring no additional
// B debit, including free estimates and lower-cost retries.
func RecordPersonalReservation(fact *types.PersonalBillingFact) error {
	return DB.Transaction(func(tx *gorm.DB) error { return applyPersonalBillingFactTx(tx, fact) })
}

// MarkPersonalRequestDispatched is replayable across transport wrappers. MySQL
// reports changed rows, so an already-unknown request must be checked explicitly.
func MarkPersonalRequestDispatched(requestID string, userID int) error {
	result := DB.Model(&ResellerRequest{}).Where("id = ? AND customer_user_id = ? AND state = ?", requestID, userID, "reserved").Update("state", "outcome_unknown")
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 1 {
		return nil
	}
	var count int64
	if err := DB.Model(&ResellerRequest{}).Where("id = ? AND customer_user_id = ? AND state = ?", requestID, userID, "outcome_unknown").Count(&count).Error; err != nil {
		return err
	}
	if count != 1 {
		return ErrResellerConflict
	}
	return nil
}

func confirmPersonalTaskExpenseTx(tx *gorm.DB, entity string, id int64, quota int, source string) error {
	if entity == TaskSettlementEntityMidjourney {
		return confirmPersonalMidjourneyExpenseTx(tx, id, quota, source)
	}
	if entity != TaskSettlementEntityTask {
		return nil
	}
	var task Task
	if err := tx.First(&task, id).Error; err != nil {
		return err
	}
	if task.PrivateData.BillingContext == nil || task.PrivateData.BillingContext.PersonalPricing == nil {
		return nil
	}
	fact := *task.PrivateData.BillingContext.PersonalPricing
	fact.OperationID = PersonalOperationID(fact.RequestID, source)
	fact.Kind = "settle"
	// Fixed-fee completion confirms the previously recorded base and exact targets.
	if quota != task.Quota {
		return ErrResellerConflict
	}
	return applyPersonalBillingFactTx(tx, &fact)
}

func adjustPersonalWalletTx(tx *gorm.DB, journal *TaskSettlementJournal) error {
	var fact types.PersonalBillingFact
	if err := common.UnmarshalJsonStr(journal.PersonalFact, &fact); err != nil {
		return err
	}
	if fact.UserID != journal.UserId || fact.CustomerTarget != int64(journal.FinalQuota) || journal.BillingSource != "wallet" {
		return ErrResellerConflict
	}
	delta := -int64(journal.Delta)
	query := tx.Model(&User{}).Where("id = ?", journal.UserId)
	if delta > 0 {
		query = query.Where("quota <= ?", common.MaxWalletQuota-delta)
	} else {
		query = query.Where("quota >= ?", common.MinWalletQuota-delta)
	}
	result := query.Update("quota", gorm.Expr("quota + ?", delta))
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return ErrWalletQuotaOutOfRange
	}
	return nil
}

func confirmPersonalMidjourneyExpenseTx(tx *gorm.DB, id int64, quota int, source string) error {
	var task Midjourney
	if err := tx.First(&task, id).Error; err != nil {
		return err
	}
	if task.PrivateData.BillingContext == nil || task.PrivateData.BillingContext.PersonalPricing == nil {
		return nil
	}
	fact := *task.PrivateData.BillingContext.PersonalPricing
	fact.OperationID = PersonalOperationID(fact.RequestID, source)
	fact.Kind = "settle"
	if quota != task.Quota {
		return ErrResellerConflict
	}
	return applyPersonalBillingFactTx(tx, &fact)
}

// Funding identity is immutable across retries and remains after journal cleanup.
type ResellerFundingSnapshot struct {
	RequestID      string `gorm:"size:64;primaryKey"`
	TokenID        int
	SubscriptionID int
	ApplyToken     bool
	BillingSource  string `gorm:"size:20"`
}

func recordPersonalFundingTx(tx *gorm.DB, params BillingBalancePreConsumeParams, subscription *SubscriptionPreConsumeResult) error {
	if params.PersonalFact == nil {
		return nil
	}
	if params.PersonalFact.UserID != params.UserId || params.PersonalFact.CustomerTarget != params.Amount || params.PersonalFact.Kind != "reserve" {
		return ErrResellerConflict
	}
	subscriptionID := 0
	if subscription != nil {
		subscriptionID = subscription.UserSubscriptionId
	}
	return tx.Create(&ResellerFundingSnapshot{SubscriptionID: subscriptionID, RequestID: params.PersonalFact.RequestID, TokenID: params.TokenId, ApplyToken: params.ApplyToken, BillingSource: params.BillingSource}).Error
}

// Generic legacy funding paths must not mutate a reseller wallet without its
// immutable fact. This is an ownership check, not a new pricing decision.
func validatePersonalFundingTx(tx *gorm.DB, userID int, source string, fact *types.PersonalBillingFact) error {
	if fact == nil {
		return RequireOrdinaryUser(tx, userID)
	}
	if fact.UserID != userID {
		return ErrResellerConflict
	}
	kind, err := ResellerUserKind(tx, userID)
	if err != nil {
		return err
	}
	switch kind {
	case "":
		if fact.AgentUserID != 0 {
			return ErrResellerConflict
		}
	case "agent_customer":
		identity, err := GetResellerIdentity(tx, userID)
		if err != nil {
			return err
		}
		if identity == nil || identity.AgentUserID != fact.AgentUserID || fact.AgentUserID <= 0 || source != "wallet" {
			return ErrResellerConflict
		}
	default:
		return ErrResellerForbidden
	}
	return nil
}
