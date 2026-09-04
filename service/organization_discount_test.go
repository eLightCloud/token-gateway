package service

import (
	"testing"

	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func seedDiscountServiceFixture(t *testing.T, organizationId, userId, snapshotId int, discounts map[int]int) {
	t.Helper()
	truncate(t)
	require.NoError(t, model.DB.Where("id = ?", organizationId).Delete(&model.Organization{}).Error)
	require.NoError(t, model.DB.Where("organization_id = ?", organizationId).Delete(&model.OrganizationMember{}).Error)
	require.NoError(t, model.DB.Where("organization_id = ?", organizationId).Delete(&model.OrganizationDiscountSnapshot{}).Error)
	require.NoError(t, model.DB.Create(&model.Organization{Id: organizationId, Name: "discount-service-org", Status: model.OrganizationStatusEnabled}).Error)
	currentKey := "42"
	require.NoError(t, model.DB.Create(&model.OrganizationMember{
		OrganizationId: organizationId,
		UserId:         userId,
		Role:           model.OrganizationRoleMember,
		CurrentKey:     &currentKey,
	}).Error)
	if discounts == nil {
		return
	}
	data, err := model.MarshalOrganizationChannelDiscounts(discounts)
	require.NoError(t, err)
	snapshot := &model.OrganizationDiscountSnapshot{
		Id:               snapshotId,
		OrganizationId:   organizationId,
		ChannelDiscounts: data,
	}
	require.NoError(t, model.DB.Create(snapshot).Error)
	require.NoError(t, model.DB.Model(&model.Organization{}).
		Where("id = ?", organizationId).
		Update("current_discount_snapshot_id", snapshot.Id).Error)
}

func TestLoadOrganizationDiscountSnapshotDefaultsToOne(t *testing.T) {
	// 无组织用户：显式已解析且折扣为 1.0
	seedDiscountServiceFixture(t, 500, 42, 0, nil)
	snapshot, err := LoadOrganizationDiscountSnapshot(999)
	require.NoError(t, err)
	assert.Nil(t, snapshot)

	// 有组织但无快照
	require.NoError(t, model.DB.Exec("DELETE FROM organization_discount_snapshots").Error)
	require.NoError(t, model.DB.Model(&model.Organization{}).
		Where("id = ?", 500).Update("current_discount_snapshot_id", 0).Error)
	snapshot, err = LoadOrganizationDiscountSnapshot(42)
	require.NoError(t, err)
	assert.Nil(t, snapshot)
}

func TestLoadOrganizationDiscountSnapshotReturnsFullMap(t *testing.T) {
	seedDiscountServiceFixture(t, 501, 42, 21, map[int]int{12: 800_000, 35: 950_000})
	snapshot, err := LoadOrganizationDiscountSnapshot(42)
	require.NoError(t, err)
	require.NotNil(t, snapshot)
	assert.EqualValues(t, 21, snapshot.SnapshotID)
	assert.InDelta(t, 0.8, snapshot.ChannelDiscounts[12], 1e-12)
	assert.InDelta(t, 0.95, snapshot.ChannelDiscounts[35], 1e-12)
	// 未配置渠道在内存映射查找时为 1.0
	assert.InDelta(t, 1.0, snapshot.RatioForChannel(99), 1e-12)
	assert.InDelta(t, 1.0, snapshot.EffectiveRatio(), 1e-12)
}

func TestLoadOrganizationDiscountSnapshotFailsClosedOnCorruption(t *testing.T) {
	seedDiscountServiceFixture(t, 502, 42, 22, map[int]int{12: 800_000})
	require.NoError(t, model.DB.Model(&model.OrganizationDiscountSnapshot{}).
		Where("id = ?", 22).
		Update("channel_discounts", "{corrupted").Error)

	_, err := LoadOrganizationDiscountSnapshot(42)
	require.ErrorIs(t, err, ErrOrganizationDiscountLoadFailed)
}

func TestApplyOrganizationDiscountForChannelUsesInMemoryMapOnly(t *testing.T) {
	snapshot := &types.OrganizationDiscountSnapshot{
		SnapshotID:       21,
		ChannelDiscounts: map[int]float64{12: 0.8, 35: 0.95},
	}
	info := &relaycommon.RelayInfo{}
	info.PriceData.DiscountSnapshot = snapshot

	ApplyOrganizationDiscountForChannel(info, 12)
	assert.InDelta(t, 0.8, snapshot.EffectiveRatio(), 1e-12)
	assert.EqualValues(t, 12, snapshot.AppliedChannelId)

	// 重试切换渠道：同一内存映射中的新渠道倍率，未配置渠道为 1.0
	ApplyOrganizationDiscountForChannel(info, 35)
	assert.InDelta(t, 0.95, snapshot.EffectiveRatio(), 1e-12)
	ApplyOrganizationDiscountForChannel(info, 99)
	assert.InDelta(t, 1.0, snapshot.EffectiveRatio(), 1e-12)

	// 无折扣快照的请求是安全 no-op
	emptyInfo := &relaycommon.RelayInfo{}
	ApplyOrganizationDiscountForChannel(emptyInfo, 12)
	assert.InDelta(t, 1.0, emptyInfo.PriceData.DiscountSnapshot.EffectiveRatio(), 1e-12)
}

func TestPrepareOrganizationDiscountReservationUsesSelectedChannelMarkup(t *testing.T) {
	billing := &recordingBillingSettler{preConsumedQuota: 100}
	snapshot := &types.OrganizationDiscountSnapshot{
		ChannelDiscounts: map[int]float64{12: 0.8, 35: 2.5},
	}
	info := &relaycommon.RelayInfo{Billing: billing}
	info.PriceData.QuotaToPreConsume = 100
	info.PriceData.DiscountSnapshot = snapshot

	ApplyOrganizationDiscountForChannel(info, 12)
	require.Nil(t, PrepareOrganizationDiscountReservation(info))
	assert.Empty(t, billing.reserveTargets)

	ApplyOrganizationDiscountForChannel(info, 35)
	require.Nil(t, PrepareOrganizationDiscountReservation(info))
	assert.EqualValues(t, []int{250}, billing.reserveTargets)
	assert.EqualValues(t, 250, info.FinalPreConsumedQuota)
}

func TestResolveOrganizationDiscountSnapshotForChannelKeepsRequestMap(t *testing.T) {
	seedDiscountServiceFixture(t, 503, 42, 23, map[int]int{12: 800_000})

	// 未解析的请求：从数据库一次性加载并钉住渠道
	priceData := types.PriceData{}
	resolved, err := ResolveOrganizationDiscountSnapshotForChannel(priceData, 42, 12)
	require.NoError(t, err)
	require.NotNil(t, resolved)
	assert.InDelta(t, 0.8, resolved.EffectiveRatio(), 1e-12)

	// 已解析的请求：管理员保存新折扣后，重试仍复用原内存映射，不重新读库
	seedDiscountServiceFixture(t, 503, 42, 24, map[int]int{12: 100_000, 35: 500_000})
	priceData.DiscountSnapshot = resolved
	priceData.DiscountSnapshotLoaded = true
	retrySnapshot, err := ResolveOrganizationDiscountSnapshotForChannel(priceData, 42, 35)
	require.NoError(t, err)
	require.NotNil(t, retrySnapshot)
	assert.EqualValues(t, 23, retrySnapshot.SnapshotID)
	assert.InDelta(t, 1.0, retrySnapshot.EffectiveRatio(), 1e-12, "channel 35 was not configured in snapshot 23")

	// 已解析且无折扣的请求保持 nil
	noDiscount := types.PriceData{DiscountSnapshotLoaded: true}
	stillNil, err := ResolveOrganizationDiscountSnapshotForChannel(noDiscount, 42, 12)
	require.NoError(t, err)
	assert.Nil(t, stillNil)
}

func TestOrganizationDiscountSettleHelpersDefaultToOne(t *testing.T) {
	info := &relaycommon.RelayInfo{}
	assert.InDelta(t, 1.0, organizationDiscountRatioFloat(info.PriceData), 1e-12)
	assert.True(t, decimal.NewFromInt(1).Equal(organizationDiscountMultiplier(info.PriceData)))

	info.PriceData.DiscountSnapshot = &types.OrganizationDiscountSnapshot{
		ChannelDiscounts: map[int]float64{12: 0.8},
	}
	assert.InDelta(t, 1.0, organizationDiscountRatioFloat(info.PriceData), 1e-12)

	ApplyOrganizationDiscountForChannel(info, 12)
	assert.InDelta(t, 0.8, organizationDiscountRatioFloat(info.PriceData), 1e-12)
}

func TestAppendOrganizationDiscountBillingInfoShape(t *testing.T) {
	info := &relaycommon.RelayInfo{}
	info.PriceData.GroupRatioInfo.GroupRatio = 1.5
	info.PriceData.DiscountSnapshot = &types.OrganizationDiscountSnapshot{
		SnapshotID:       21,
		ChannelDiscounts: map[int]float64{12: 0.8},
	}
	ApplyOrganizationDiscountForChannel(info, 12)

	other := model.NewLogOther()
	AppendOrganizationDiscountBillingInfo(info, other)

	rendered := other.Snapshot()
	assert.InDelta(t, 1.2, rendered["effective_ratio"], 1e-12)
	adminInfo, ok := rendered["admin_info"].(map[string]interface{})
	require.True(t, ok)
	discount, ok := adminInfo["organization_discount"].(map[string]interface{})
	require.True(t, ok)
	assert.EqualValues(t, 21, discount["snapshot_id"])
	assert.EqualValues(t, 12, discount["channel_id"])
	assert.InDelta(t, 0.8, discount["ratio"], 1e-12)

	// 无折扣请求不写任何折扣字段
	emptyOther := model.NewLogOther()
	AppendOrganizationDiscountBillingInfo(&relaycommon.RelayInfo{}, emptyOther)
	assert.NotContains(t, emptyOther.Snapshot(), "effective_ratio")
}

func TestPreWssConsumeQuotaRequiresResolvedDiscountSnapshot(t *testing.T) {
	err := PreWssConsumeQuota(nil, &relaycommon.RelayInfo{}, nil)
	require.ErrorIs(t, err, ErrOrganizationDiscountLoadFailed)
}
