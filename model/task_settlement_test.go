package model

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

type legacyLogTable struct {
	Id int
}

func (legacyLogTable) TableName() string { return "logs" }

func TestLogBillingOperationIndexMigratesExistingSQLiteTable(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, db.AutoMigrate(&legacyLogTable{}))
	require.NoError(t, db.AutoMigrate(&Log{}))
	require.NoError(t, migrateLogBillingOperationIndex(db))
	require.NoError(t, migrateLogBillingOperationIndex(db))
	assert.True(t, db.Migrator().HasColumn(&Log{}, "billing_operation_id"))
	assert.True(t, db.Migrator().HasIndex(&Log{}, logBillingOperationIndex))

	operationId := "settlement:1"
	require.NoError(t, db.Create(&Log{BillingOperationId: &operationId}).Error)
	require.Error(t, db.Create(&Log{BillingOperationId: &operationId}).Error)
	require.NoError(t, db.Create(&Log{}).Error)
	require.NoError(t, db.Create(&Log{}).Error)
}

func TestTaskSettlementJournalAppliesBalancesAndTaskQuotaAtomically(t *testing.T) {
	truncateTables(t)
	user := &User{Id: 70, Username: "journal-user", Quota: 1_000}
	token := &Token{Id: 70, UserId: user.Id, Key: "sk-journal", RemainQuota: 500, UsedQuota: 100}
	task := &Task{TaskID: "journal-task", UserId: user.Id, Quota: 300, Status: TaskStatusFailure}
	require.NoError(t, DB.Create(user).Error)
	require.NoError(t, DB.Create(token).Error)
	require.NoError(t, DB.Create(task).Error)

	journal := &TaskSettlementJournal{
		RequestId: "task-refund:atomic", Operation: TaskSettlementOperationRefund,
		EntityType: TaskSettlementEntityTask, EntityId: task.ID,
		EntityExpectedQuota: 300, EntityFinalQuota: 0,
		ExpectedQuota: 300, FinalQuota: 0, Delta: -300,
		UserId: user.Id, TokenId: token.Id, BillingSource: "wallet",
		ApplyToken: true,
	}
	_, err := EnsureTaskSettlementJournal(journal)
	require.NoError(t, err)
	stored, appliedNow, err := ApplyTaskSettlementJournal(journal.RequestId)
	require.NoError(t, err)
	assert.True(t, appliedNow)
	assert.True(t, stored.Applied)

	require.NoError(t, DB.First(user, user.Id).Error)
	require.NoError(t, DB.First(token, token.Id).Error)
	require.NoError(t, DB.First(task, task.ID).Error)
	assert.EqualValues(t, 1_300, user.Quota)
	assert.EqualValues(t, 800, token.RemainQuota)
	assert.EqualValues(t, -200, token.UsedQuota)
	assert.Zero(t, task.Quota)

	// A replay reads Applied and cannot repeat any balance update.
	_, appliedNow, err = ApplyTaskSettlementJournal(journal.RequestId)
	require.NoError(t, err)
	assert.False(t, appliedNow)
	require.NoError(t, DB.First(user, user.Id).Error)
	assert.EqualValues(t, 1_300, user.Quota)
}

func TestTaskSettlementJournalRollsBackEveryStepOnFundingFailure(t *testing.T) {
	truncateTables(t)
	user := &User{Id: 71, Username: "journal-rollback", Quota: 1_000}
	token := &Token{Id: 71, UserId: user.Id, Key: "sk-rollback", RemainQuota: 500}
	task := &Task{TaskID: "journal-rollback", UserId: user.Id, Quota: 300}
	require.NoError(t, DB.Create(user).Error)
	require.NoError(t, DB.Create(token).Error)
	require.NoError(t, DB.Create(task).Error)

	journal := &TaskSettlementJournal{
		RequestId: "task-refund:rollback", Operation: TaskSettlementOperationRefund,
		EntityType: TaskSettlementEntityTask, EntityId: task.ID,
		EntityExpectedQuota: 300, EntityFinalQuota: 0,
		ExpectedQuota: 300, FinalQuota: 0, Delta: -300,
		UserId: user.Id, TokenId: token.Id, BillingSource: "subscription",
		SubscriptionId: 9999, ApplyToken: true,
	}
	_, err := EnsureTaskSettlementJournal(journal)
	require.NoError(t, err)
	_, _, err = ApplyTaskSettlementJournal(journal.RequestId)
	require.Error(t, err)

	require.NoError(t, DB.First(user, user.Id).Error)
	require.NoError(t, DB.First(token, token.Id).Error)
	require.NoError(t, DB.First(task, task.ID).Error)
	assert.EqualValues(t, 1_000, user.Quota)
	assert.EqualValues(t, 500, token.RemainQuota)
	assert.Equal(t, 300, task.Quota)
	pending, listErr := ListPendingTaskSettlementJournals(10)
	require.NoError(t, listErr)
	require.Len(t, pending, 1)
	assert.False(t, pending[0].Applied)
}

func TestTaskSettlementJournalRejectsChangedBillingFact(t *testing.T) {
	truncateTables(t)
	base := &TaskSettlementJournal{
		RequestId: "fact-conflict", Operation: TaskSettlementOperationSettle,
		ExpectedQuota: 100, FinalQuota: 80, Delta: -20,
		UserId: 72, BillingSource: "wallet", LogDone: true,
	}
	_, err := EnsureTaskSettlementJournal(base)
	require.NoError(t, err)
	changed := *base
	changed.FinalQuota = 70
	changed.Delta = -30
	_, err = EnsureTaskSettlementJournal(&changed)
	require.ErrorIs(t, err, ErrTaskSettlementJournalConflict)
}

func TestRecordTaskBillingLogIsIdempotentByOperation(t *testing.T) {
	truncateTables(t)
	user := &User{Id: 73, Username: "log-user", Quota: 1_000}
	require.NoError(t, DB.Create(user).Error)
	params := RecordTaskBillingLogParams{
		OperationId: "task-refund:log-once", UserId: user.Id,
		LogType: LogTypeRefund, Quota: 100, ModelName: "test-model",
	}
	require.NoError(t, RecordTaskBillingLog(params))
	require.NoError(t, RecordTaskBillingLog(params))
	var count int64
	require.NoError(t, DB.Model(&Log{}).
		Where("billing_operation_id = ?", params.OperationId).Count(&count).Error)
	assert.EqualValues(t, 1, count)
}

func TestListRefundPendingFailures(t *testing.T) {
	truncateTables(t)
	assert.False(t, HasRefundPendingFailures())

	failed := &Task{TaskID: "task_pending_refund", Status: TaskStatusFailure, Quota: 100}
	require.NoError(t, DB.Create(failed).Error)
	succeeded := &Task{TaskID: "task_settled", Status: TaskStatusSuccess, Quota: 100}
	require.NoError(t, DB.Create(succeeded).Error)
	mjFailed := &Midjourney{UserId: 9, MjId: "mj_pending_refund", Status: "FAILURE", Quota: 50, Progress: "100%"}
	require.NoError(t, DB.Create(mjFailed).Error)

	assert.True(t, HasRefundPendingFailures())
	tasks, err := ListRefundPendingTasks(10)
	require.NoError(t, err)
	require.Len(t, tasks, 1)
	assert.Equal(t, "task_pending_refund", tasks[0].TaskID)
	mjTasks, err := ListRefundPendingMidjourneyTasks(10)
	require.NoError(t, err)
	require.Len(t, mjTasks, 1)
	assert.Equal(t, "mj_pending_refund", mjTasks[0].MjId)
}
