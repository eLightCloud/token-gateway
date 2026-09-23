package helper

import (
	"net/http/httptest"
	"testing"

	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relaytypes "github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRepricingPreservesFrozenOrganizationDiscount(t *testing.T) {
	saved := map[string]string{}
	require.NoError(t, config.GlobalConfig.SaveToDB(func(key, value string) error {
		saved[key] = value
		return nil
	}))
	prices := ratio_setting.ModelPrice2JSONString()
	t.Cleanup(func() {
		require.NoError(t, config.GlobalConfig.LoadFromDB(saved))
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(prices))
	})
	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		"billing_setting.billing_mode": `{"discount-tiered":"tiered_expr"}`,
		"billing_setting.billing_expr": `{"discount-tiered":"tier(\"request\",fixed(0.01))"}`,
	}))
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(`{"discount-legacy":0.01}`))
	for _, modelName := range []string{"discount-tiered", "discount-legacy"} {
		for _, hasSnapshot := range []bool{false, true} {
			t.Run(modelName+map[bool]string{false: "/no discount", true: "/configured discount"}[hasSnapshot], func(t *testing.T) {
				ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
				ctx.Request = httptest.NewRequest("POST", "/v1/responses/compact", nil)
				info := &relaycommon.RelayInfo{OriginModelName: modelName, UserGroup: "default", UsingGroup: "default"}
				info.PriceData.DiscountSnapshotLoaded = true
				if hasSnapshot {
					info.PriceData.DiscountSnapshot = &types.OrganizationDiscountSnapshot{SnapshotID: 4, ChannelDiscounts: map[int]float64{12: 0.8, 35: 1.5}}
					service.ApplyOrganizationDiscountForChannel(info, 12)
				}
				_, err := ModelPriceHelper(ctx, info, 100, &relaytypes.TokenCountMeta{})
				require.NoError(t, err)
				require.True(t, info.PriceData.DiscountSnapshotLoaded, "repricing must not reload organization configuration")
				if hasSnapshot {
					require.NotNil(t, info.PriceData.DiscountSnapshot)
					assert.Equal(t, 4, info.PriceData.DiscountSnapshot.SnapshotID)
					assert.Equal(t, 0.8, info.PriceData.DiscountSnapshot.EffectiveRatio())
					service.ApplyOrganizationDiscountForChannel(info, 35)
					assert.Equal(t, 1.5, info.PriceData.DiscountSnapshot.EffectiveRatio(), "retry must use the same frozen channel map")
				} else {
					assert.Nil(t, info.PriceData.DiscountSnapshot)
				}
			})
		}
	}
}
