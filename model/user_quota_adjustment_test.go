package model

import (
	"math"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestApplyAdminUserQuotaAdjustmentPersistsExactWalletDeltas(t *testing.T) {
	setupOrganizationTestState(t)
	require.NoError(t, DB.Create(&User{
		Id:       20,
		Username: "adjusted-user",
		Password: "password",
		Status:   common.UserStatusEnabled,
		Quota:    1_000_000,
	}).Error)

	added, err := ApplyAdminUserQuotaAdjustment(20, 1, UserQuotaAdjustmentModeAdd, 500_000)
	require.NoError(t, err)
	assert.Equal(t, 1_000_000, added.PreviousQuota)
	assert.Equal(t, 1_500_000, added.CurrentQuota)
	assert.Equal(t, 500_000, added.DeltaQuota)

	subtracted, err := ApplyAdminUserQuotaAdjustment(20, 1, UserQuotaAdjustmentModeSubtract, 250_000)
	require.NoError(t, err)
	assert.Equal(t, -250_000, subtracted.DeltaQuota)

	overridden, err := ApplyAdminUserQuotaAdjustment(20, 1, UserQuotaAdjustmentModeOverride, 2_000_000)
	require.NoError(t, err)
	assert.Equal(t, 750_000, overridden.DeltaQuota)
	assert.Equal(t, 2_000_000, overridden.CurrentQuota)

	var storedUser User
	require.NoError(t, DB.Select("quota").First(&storedUser, 20).Error)
	assert.Equal(t, 2_000_000, storedUser.Quota)

	var adjustments []UserQuotaAdjustment
	require.NoError(t, DB.Where("user_id = ?", 20).Order("id").Find(&adjustments).Error)
	require.Len(t, adjustments, 3)
	assert.Equal(t, []int64{500_000, -250_000, 750_000}, []int64{
		adjustments[0].DeltaQuota,
		adjustments[1].DeltaQuota,
		adjustments[2].DeltaQuota,
	})
}

func TestApplyAdminUserQuotaAdjustmentRejectsOverflowWithoutPartialWrite(t *testing.T) {
	setupOrganizationTestState(t)
	require.NoError(t, DB.Create(&User{
		Id:       21,
		Username: "quota-limit-user",
		Password: "password",
		Status:   common.UserStatusEnabled,
		Quota:    math.MaxInt32,
	}).Error)

	_, err := ApplyAdminUserQuotaAdjustment(21, 1, UserQuotaAdjustmentModeAdd, 1)
	require.Error(t, err)

	var stored User
	require.NoError(t, DB.Select("quota").First(&stored, 21).Error)
	assert.Equal(t, math.MaxInt32, stored.Quota)
	var count int64
	require.NoError(t, DB.Model(&UserQuotaAdjustment{}).Where("user_id = ?", 21).Count(&count).Error)
	assert.Zero(t, count)
}
