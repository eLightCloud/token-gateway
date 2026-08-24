package model

import (
	"fmt"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func seedDiscountFixture(t *testing.T, organizationId, userId int, channelIds ...int) {
	t.Helper()
	truncateTables(t)
	require.NoError(t, DB.Create(&Organization{Id: organizationId, Name: "discount-org", Status: OrganizationStatusEnabled}).Error)
	require.NoError(t, DB.Create(&User{Id: userId, Username: "discount-root", Role: common.RoleRootUser, Status: common.UserStatusEnabled}).Error)
	for _, channelId := range channelIds {
		require.NoError(t, DB.Create(&Channel{Id: channelId, Name: fmt.Sprintf("channel-%d", channelId), Key: "test", Status: common.ChannelStatusEnabled}).Error)
	}
}

func saveDiscounts(t *testing.T, organizationId, userId, expectedSnapshotId int, items ...OrganizationChannelDiscountParam) *OrganizationDiscountSnapshot {
	t.Helper()
	snapshot, err := SaveOrganizationDiscount(SaveOrganizationDiscountParams{
		OrganizationId:     organizationId,
		ChannelDiscounts:   items,
		ExpectedSnapshotId: expectedSnapshotId,
		CreatedBy:          userId,
	})
	require.NoError(t, err)
	require.NotNil(t, snapshot)
	return snapshot
}

func TestParseOrganizationDiscountRatio(t *testing.T) {
	cases := []struct {
		input   string
		want    int
		wantErr bool
	}{
		{input: "0.8", want: 800_000},
		{input: "1", want: 1_000_000},
		{input: "0.123456", want: 123_456},
		{input: "0.000001", want: 1},
		// 超出六位小数必须报错，禁止静默截断
		{input: "0.1234567", wantErr: true},
		{input: "abc", wantErr: true},
		{input: "", wantErr: true},
		// 范围在 decimal 上先于整数转换校验，超大值不得窄化后落入合法区间
		{input: "0", wantErr: true},
		{input: "-0.5", wantErr: true},
		{input: "1.000001", wantErr: true},
		{input: "2", wantErr: true},
		{input: "999999999999999999999999", wantErr: true},
	}
	for _, tc := range cases {
		got, err := ParseOrganizationDiscountRatio(tc.input)
		if tc.wantErr {
			require.Error(t, err, "input %q", tc.input)
			continue
		}
		require.NoError(t, err, "input %q", tc.input)
		assert.Equal(t, tc.want, got, "input %q", tc.input)
	}
}

func TestFormatOrganizationDiscountRatio(t *testing.T) {
	assert.Equal(t, "0.85", FormatOrganizationDiscountRatio(850_000))
	assert.Equal(t, "1", FormatOrganizationDiscountRatio(1_000_000))
	assert.Equal(t, "0.123456", FormatOrganizationDiscountRatio(123_456))
}

func TestValidateOrganizationDiscountRatioScaled(t *testing.T) {
	assert.NoError(t, ValidateOrganizationDiscountRatioScaled(1))
	assert.NoError(t, ValidateOrganizationDiscountRatioScaled(1_000_000))
	assert.Error(t, ValidateOrganizationDiscountRatioScaled(0))
	assert.Error(t, ValidateOrganizationDiscountRatioScaled(-1))
	assert.Error(t, ValidateOrganizationDiscountRatioScaled(1_000_001))
}

func TestMarshalOrganizationChannelDiscountsNormalizesOne(t *testing.T) {
	data, err := MarshalOrganizationChannelDiscounts(map[int]int{
		12: 800_000,
		35: 1_000_000, // 1.0 归一化为未配置
	})
	require.NoError(t, err)
	assert.JSONEq(t, `{"12":800000}`, data)

	empty, err := MarshalOrganizationChannelDiscounts(map[int]int{})
	require.NoError(t, err)
	assert.Equal(t, "{}", empty)
}

func TestUnmarshalOrganizationChannelDiscountsFailsClosedOnCorruption(t *testing.T) {
	discounts, err := UnmarshalOrganizationChannelDiscounts(`{"12":800000,"35":950000}`)
	require.NoError(t, err)
	assert.Equal(t, map[int]int{12: 800_000, 35: 950_000}, discounts)

	_, err = UnmarshalOrganizationChannelDiscounts(`{not-json`)
	require.ErrorIs(t, err, ErrOrganizationDiscountInvalidJSON)

	// 非正整数渠道键视为损坏数据
	_, err = UnmarshalOrganizationChannelDiscounts(`{"x":800000}`)
	require.ErrorIs(t, err, ErrOrganizationDiscountInvalidJSON)

	// 合法 JSON 中的非法倍率值同样视为损坏，不得静默按 1.0 计费
	for _, payload := range []string{
		`{"12":0}`,
		`{"12":-5}`,
		`{"12":1000001}`,
	} {
		_, err = UnmarshalOrganizationChannelDiscounts(payload)
		require.ErrorIs(t, err, ErrOrganizationDiscountInvalidJSON, payload)
	}
}

func TestSaveOrganizationDiscountRejectsInvalidInput(t *testing.T) {
	seedDiscountFixture(t, 600, 1, 900)

	_, err := SaveOrganizationDiscount(SaveOrganizationDiscountParams{
		OrganizationId: 600,
		ChannelDiscounts: []OrganizationChannelDiscountParam{
			{ChannelId: 900, RatioScaled: 800_000},
			{ChannelId: 900, RatioScaled: 900_000},
		},
	})
	require.ErrorIs(t, err, ErrOrganizationDiscountDuplicateChannel)

	_, err = SaveOrganizationDiscount(SaveOrganizationDiscountParams{
		OrganizationId: 600,
		ChannelDiscounts: []OrganizationChannelDiscountParam{
			{ChannelId: 901, RatioScaled: 800_000},
		},
	})
	require.ErrorIs(t, err, ErrOrganizationDiscountInvalidChannel)

	_, err = SaveOrganizationDiscount(SaveOrganizationDiscountParams{
		OrganizationId: 600,
		ChannelDiscounts: []OrganizationChannelDiscountParam{
			{ChannelId: 0, RatioScaled: 800_000},
		},
	})
	require.ErrorIs(t, err, ErrOrganizationDiscountInvalidChannel)

	_, err = SaveOrganizationDiscount(SaveOrganizationDiscountParams{
		OrganizationId: 600,
		ChannelDiscounts: []OrganizationChannelDiscountParam{
			{ChannelId: 900, RatioScaled: 0},
		},
	})
	require.ErrorIs(t, err, ErrOrganizationDiscountInvalidRatio)

	_, err = SaveOrganizationDiscount(SaveOrganizationDiscountParams{
		OrganizationId: 600,
		ChannelDiscounts: []OrganizationChannelDiscountParam{
			{ChannelId: 900, RatioScaled: 1_000_001},
		},
	})
	require.ErrorIs(t, err, ErrOrganizationDiscountInvalidRatio)

	_, err = SaveOrganizationDiscount(SaveOrganizationDiscountParams{OrganizationId: 999})
	require.ErrorIs(t, err, ErrOrganizationDiscountOrganizationNotFound)

	// 失败的保存不产生任何快照
	var count int64
	require.NoError(t, DB.Model(&OrganizationDiscountSnapshot{}).Where("organization_id = ?", 600).Count(&count).Error)
	assert.Zero(t, count)
}

func TestSaveOrganizationDiscountNormalizesAndClears(t *testing.T) {
	seedDiscountFixture(t, 601, 1, 900, 901)

	first := saveDiscounts(t, 601, 1, 0,
		OrganizationChannelDiscountParam{ChannelId: 900, RatioScaled: 800_000},
		OrganizationChannelDiscountParam{ChannelId: 901, RatioScaled: 1_000_000}, // 1.0 → 未配置
	)
	current, err := GetCurrentOrganizationDiscountSnapshot(601)
	require.NoError(t, err)
	require.NotNil(t, current)
	assert.Equal(t, first.Id, current.Id)
	discounts, err := UnmarshalOrganizationChannelDiscounts(current.ChannelDiscounts)
	require.NoError(t, err)
	assert.Equal(t, map[int]int{900: 800_000}, discounts)

	// 空集合清除全部组织折扣：新快照仍追加，当前指针指向空配置
	cleared := saveDiscounts(t, 601, 1, first.Id)
	current, err = GetCurrentOrganizationDiscountSnapshot(601)
	require.NoError(t, err)
	require.NotNil(t, current)
	assert.Equal(t, cleared.Id, current.Id)
	discounts, err = UnmarshalOrganizationChannelDiscounts(current.ChannelDiscounts)
	require.NoError(t, err)
	assert.Empty(t, discounts)
}

func TestSaveOrganizationDiscountOptimisticLockConflictRollsBack(t *testing.T) {
	seedDiscountFixture(t, 602, 1, 900)

	first := saveDiscounts(t, 602, 1, 0,
		OrganizationChannelDiscountParam{ChannelId: 900, RatioScaled: 800_000},
	)
	second := saveDiscounts(t, 602, 1, first.Id,
		OrganizationChannelDiscountParam{ChannelId: 900, RatioScaled: 900_000},
	)

	// 基于过期 expected_snapshot_id 提交必须失败
	_, err := SaveOrganizationDiscount(SaveOrganizationDiscountParams{
		OrganizationId:     602,
		ExpectedSnapshotId: first.Id,
		ChannelDiscounts: []OrganizationChannelDiscountParam{
			{ChannelId: 900, RatioScaled: 500_000},
		},
	})
	require.ErrorIs(t, err, ErrOrganizationDiscountSnapshotConflict)

	// 冲突事务不得残留历史快照，也不得覆盖已提交数据
	var count int64
	require.NoError(t, DB.Model(&OrganizationDiscountSnapshot{}).Where("organization_id = ?", 602).Count(&count).Error)
	assert.Equal(t, int64(2), count)
	current, err := GetCurrentOrganizationDiscountSnapshot(602)
	require.NoError(t, err)
	require.NotNil(t, current)
	assert.Equal(t, second.Id, current.Id)
	discounts, err := UnmarshalOrganizationChannelDiscounts(current.ChannelDiscounts)
	require.NoError(t, err)
	assert.Equal(t, map[int]int{900: 900_000}, discounts)
}

func TestSaveOrganizationDiscountSerializesConcurrentSaves(t *testing.T) {
	seedDiscountFixture(t, 603, 1, 900)

	first := saveDiscounts(t, 603, 1, 0,
		OrganizationChannelDiscountParam{ChannelId: 900, RatioScaled: 800_000},
	)

	const workers = 2
	var wg sync.WaitGroup
	errs := make([]error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			_, errs[index] = SaveOrganizationDiscount(SaveOrganizationDiscountParams{
				OrganizationId:     603,
				ExpectedSnapshotId: first.Id,
				ChannelDiscounts: []OrganizationChannelDiscountParam{
					{ChannelId: 900, RatioScaled: 500_000 + 100_000*index},
				},
				CreatedBy: 1,
			})
		}(i)
	}
	wg.Wait()

	conflicts := 0
	for _, err := range errs {
		if err == nil {
			continue
		}
		if assert.ErrorIs(t, err, ErrOrganizationDiscountSnapshotConflict) {
			conflicts++
		}
	}
	assert.Equal(t, workers-1, conflicts, "only one concurrent save may win")

	// 成功者数量 + 首份快照 = 全部历史，失败者不残留
	var count int64
	require.NoError(t, DB.Model(&OrganizationDiscountSnapshot{}).Where("organization_id = ?", 603).Count(&count).Error)
	assert.Equal(t, int64(2), count)
}

func TestGetCurrentOrganizationDiscountSnapshotForUser(t *testing.T) {
	seedDiscountFixture(t, 604, 1, 900)
	currentKey := "1"
	require.NoError(t, DB.Create(&OrganizationMember{
		OrganizationId: 604,
		UserId:         1,
		Role:           OrganizationRoleMember,
		CurrentKey:     &currentKey,
	}).Error)

	// 无快照 → nil, nil
	snapshot, err := GetCurrentOrganizationDiscountSnapshotForUser(1)
	require.NoError(t, err)
	assert.Nil(t, snapshot)

	created := saveDiscounts(t, 604, 1, 0,
		OrganizationChannelDiscountParam{ChannelId: 900, RatioScaled: 800_000},
	)
	snapshot, err = GetCurrentOrganizationDiscountSnapshotForUser(1)
	require.NoError(t, err)
	require.NotNil(t, snapshot)
	assert.Equal(t, created.Id, snapshot.Id)

	// 无组织用户 → nil, nil
	snapshot, err = GetCurrentOrganizationDiscountSnapshotForUser(404)
	require.NoError(t, err)
	assert.Nil(t, snapshot)
}

func TestGetOrganizationDiscountHistoryDerivesChangesAcrossPages(t *testing.T) {
	seedDiscountFixture(t, 605, 1, 900, 901)

	first := saveDiscounts(t, 605, 1, 0,
		OrganizationChannelDiscountParam{ChannelId: 900, RatioScaled: 800_000},
	)
	second := saveDiscounts(t, 605, 1, first.Id,
		OrganizationChannelDiscountParam{ChannelId: 900, RatioScaled: 900_000},
		OrganizationChannelDiscountParam{ChannelId: 901, RatioScaled: 950_000},
	)
	third := saveDiscounts(t, 605, 1, second.Id,
		OrganizationChannelDiscountParam{ChannelId: 901, RatioScaled: 950_000},
	)

	// 第一页（page_size=2）：最新两份，最旧一份（second）的前态来自跨页读取的 first
	pageOne, err := GetOrganizationDiscountHistory(605, 0, 2)
	require.NoError(t, err)
	require.Len(t, pageOne.Items, 2)
	assert.Equal(t, int64(3), pageOne.Total)

	thirdItem := pageOne.Items[0]
	assert.Equal(t, third.Id, thirdItem.Snapshot.Id)
	// third 相对 second：仅渠道 900 被移除（second 有 900+901，third 只有 901）
	require.Len(t, thirdItem.Changes, 1)
	assert.Equal(t, 900, thirdItem.Changes[0].ChannelId)
	assert.Equal(t, 800_000+100_000, thirdItem.Changes[0].OldScaled)
	assert.Zero(t, thirdItem.Changes[0].NewScaled)

	secondItem := pageOne.Items[1]
	assert.Equal(t, second.Id, secondItem.Snapshot.Id)
	require.Len(t, secondItem.Changes, 2)
	assert.Equal(t, 900, secondItem.Changes[0].ChannelId)
	assert.Equal(t, 800_000, secondItem.Changes[0].OldScaled)
	assert.Equal(t, 900_000, secondItem.Changes[0].NewScaled)
	assert.Equal(t, 901, secondItem.Changes[1].ChannelId)
	assert.Zero(t, secondItem.Changes[1].OldScaled)
	assert.Equal(t, 950_000, secondItem.Changes[1].NewScaled)

	// 第二页：组织第一份快照，只有它以空配置作为变更前状态
	pageTwo, err := GetOrganizationDiscountHistory(605, 2, 2)
	require.NoError(t, err)
	require.Len(t, pageTwo.Items, 1)
	firstItem := pageTwo.Items[0]
	assert.Equal(t, first.Id, firstItem.Snapshot.Id)
	require.Len(t, firstItem.Changes, 1)
	assert.Equal(t, 900, firstItem.Changes[0].ChannelId)
	assert.Zero(t, firstItem.Changes[0].OldScaled, "first snapshot prior state is empty config")
	assert.Equal(t, 800_000, firstItem.Changes[0].NewScaled)
}
