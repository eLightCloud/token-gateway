package model

import (
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPreviewAndApplyLegacyUserQuotaAdjustmentsIsScopedAndIdempotent(t *testing.T) {
	setupOrganizationTestState(t)
	insertOrganizationTestUser(t, 30, "legacy-member")
	insertOrganizationTestUser(t, 1, "operator")
	require.NoError(t, DB.Model(&User{}).Where("id = ?", 1).Update("role", common.RoleAdminUser).Error)
	require.NoError(t, DB.Create(&Organization{Id: 300, Name: "legacy org", Status: OrganizationStatusEnabled}).Error)
	joinTime := beijingInvoiceTimestamp(t, "2026-08-01 00:00:00")
	leaveTime := beijingInvoiceTimestamp(t, "2026-08-10 00:00:00")
	require.NoError(t, DB.Create(&OrganizationMember{
		OrganizationId: 300,
		UserId:         30,
		Role:           OrganizationRoleMember,
		JoinedAt:       joinTime,
		BillingStartAt: joinTime,
		LeftAt:         leaveTime,
	}).Error)
	inPeriod := beijingInvoiceTimestamp(t, "2026-08-05 10:00:00")
	require.NoError(t, LOG_DB.Create(&[]Log{
		{
			Id: 8001, UserId: 1, CreatedAt: inPeriod, Type: LogTypeManage,
			Other: `{"admin_info":{"admin_id":1},"op":{"action":"user.quota_add","params":{"quota":"＄1500.000000 额度","target_user_id":30}}}`,
		},
		{
			Id: 8002, UserId: 1, CreatedAt: inPeriod, Type: LogTypeManage,
			Other: `{"admin_info":{"admin_id":1},"op":{"action":"user.quota_add","params":{"quota":"＄10.000000 额度","quota_value":5000000,"adjustment_id":9,"target_user_id":30}}}`,
		},
		{
			Id: 8003, UserId: 1, CreatedAt: leaveTime, Type: LogTypeManage,
			Other: `{"admin_info":{"admin_id":1},"op":{"action":"user.quota_add","params":{"quota":"＄20.000000 额度","target_user_id":30}}}`,
		},
		{
			Id: 8004, UserId: 30, CreatedAt: inPeriod, Type: LogTypeManage,
			Content: "管理员增加用户额度 ＄300.000000 额度",
			Other:   `{"admin_info":{"admin_id":1}}`,
		},
	}).Error)

	period, err := NewOrganizationInvoicePeriod("2026-08-01", "2026-08-31", time.Now())
	require.NoError(t, err)
	candidates, err := PreviewLegacyUserQuotaAdjustments(300, period)
	require.NoError(t, err)
	require.Len(t, candidates, 2)
	assert.EqualValues(t, int64(8001), candidates[0].SourceLogId)
	assert.EqualValues(t, "1500.000000", candidates[0].AmountUSD)
	assert.EqualValues(t, int64(750_000_000), candidates[0].DeltaQuota)
	assert.False(t, candidates[0].AlreadyApplied)
	assert.EqualValues(t, int64(8004), candidates[1].SourceLogId)
	assert.EqualValues(t, "300.000000", candidates[1].AmountUSD)
	assert.EqualValues(t, int64(150_000_000), candidates[1].DeltaQuota)

	applied, err := ApplyLegacyUserQuotaAdjustmentsForOrganization(300, period, 1, inPeriod+1)
	require.NoError(t, err)
	assert.EqualValues(t, int64(2), applied)
	applied, err = ApplyLegacyUserQuotaAdjustmentsForOrganization(300, period, 1, inPeriod+2)
	require.NoError(t, err)
	assert.Zero(t, applied)

	var facts []UserQuotaAdjustmentLegacyFact
	require.NoError(t, DB.Find(&facts).Error)
	require.Len(t, facts, 2)
}

func TestParseLegacyQuotaAuditAmountFailsClosed(t *testing.T) {
	originalQuotaPerUnit := common.QuotaPerUnit
	t.Cleanup(func() { common.QuotaPerUnit = originalQuotaPerUnit })
	common.QuotaPerUnit = 500_000

	for _, value := range []string{"1500", "¥1500 额度", "＄-1 额度", "＄1e3 额度", "＄0 额度"} {
		_, _, err := parseLegacyQuotaAuditAmount(value)
		require.Error(t, err, value)
	}
}
