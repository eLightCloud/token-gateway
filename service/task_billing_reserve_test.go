package service

import (
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPrepareTaskBillingForAttemptPreConsumesMarkupOnFirstAttempt(t *testing.T) {
	truncate(t)
	gin.SetMode(gin.TestMode)
	const userID = 721
	seedUser(t, userID, 1_000)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	info := &relaycommon.RelayInfo{
		UserId:       userID,
		IsPlayground: true,
		UserSetting: dto.UserSetting{
			BillingPreference: "wallet_only",
		},
		PriceData: types.PriceData{
			QuotaToPreConsume: 100,
			Quota:             500,
		},
	}

	require.Nil(t, PrepareTaskBillingForAttempt(ctx, info))
	require.NotNil(t, info.Billing)
	assert.True(t, info.ForcePreConsume)
	assert.Equal(t, 500, info.Billing.GetPreConsumedQuota())
	userQuota, err := model.GetUserQuota(userID, false)
	require.NoError(t, err)
	assert.EqualValues(t, 500, userQuota)
}

func TestPrepareTaskBillingForAttemptReservesHigherRetryTarget(t *testing.T) {
	billing := &recordingBillingSettler{preConsumedQuota: 100}
	info := &relaycommon.RelayInfo{
		Billing: billing,
		PriceData: types.PriceData{
			QuotaToPreConsume: 100,
			Quota:             500,
		},
	}

	require.Nil(t, PrepareTaskBillingForAttempt(nil, info))
	assert.Equal(t, []int{500}, billing.reserveTargets)
	assert.Equal(t, 500, info.FinalPreConsumedQuota)
}
