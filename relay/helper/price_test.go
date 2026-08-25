package helper

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func TestModelPriceHelpersFailClosedWhenDiscountDatabaseIsUnavailable(t *testing.T) {
	previousDB := model.DB
	db, err := gorm.Open(sqlite.Open("file:discount-load-failure?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	require.NoError(t, sqlDB.Close())
	model.DB = db
	t.Cleanup(func() {
		model.DB = previousDB
	})

	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Set("group", "default")
	info := &relaycommon.RelayInfo{
		UserId:          42,
		UserGroup:       "default",
		UsingGroup:      "default",
		OriginModelName: "discount-db-error",
	}

	_, err = ModelPriceHelper(ctx, info, 0, &types.TokenCountMeta{})
	require.ErrorIs(t, err, service.ErrOrganizationDiscountLoadFailed)
	_, err = ModelPriceHelperPerCall(ctx, info)
	require.ErrorIs(t, err, service.ErrOrganizationDiscountLoadFailed)
}

func TestModelPriceHelperPerCallFailsClosedWhenSnapshotParseFails(t *testing.T) {
	previousDB := model.DB
	db, err := gorm.Open(sqlite.Open("file:discount-parse-failure?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	t.Cleanup(func() {
		model.DB = previousDB
		sqlDB, dbErr := db.DB()
		if dbErr == nil {
			_ = sqlDB.Close()
		}
	})
	require.NoError(t, db.AutoMigrate(
		&model.Organization{},
		&model.OrganizationMember{},
		&model.OrganizationDiscountSnapshot{},
	))
	org := model.Organization{Id: 77, Name: "discount-org", Status: model.OrganizationStatusEnabled}
	require.NoError(t, db.Create(&org).Error)
	currentKey := "42"
	require.NoError(t, db.Create(&model.OrganizationMember{
		OrganizationId: org.Id,
		UserId:         42,
		Role:           model.OrganizationRoleMember,
		CurrentKey:     &currentKey,
	}).Error)
	snapshot := model.OrganizationDiscountSnapshot{
		Id:               5,
		OrganizationId:   org.Id,
		ChannelDiscounts: "{corrupted",
	}
	require.NoError(t, db.Create(&snapshot).Error)
	require.NoError(t, db.Model(&org).Update("current_discount_snapshot_id", snapshot.Id).Error)

	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Set("group", "default")
	info := &relaycommon.RelayInfo{
		UserId:          42,
		UserGroup:       "default",
		UsingGroup:      "default",
		OriginModelName: "discount-parse-error",
		ChannelMeta:     &relaycommon.ChannelMeta{ChannelId: 99},
	}

	_, err = ModelPriceHelperPerCall(ctx, info)
	require.ErrorIs(t, err, service.ErrOrganizationDiscountLoadFailed)
}

func TestModelPriceHelperPerCallDualQuotaAndRequestSnapshotAcrossRetries(t *testing.T) {
	previousDB := model.DB
	db, err := gorm.Open(sqlite.Open("file:discount-retry-snapshot?mode=memory&cache=shared"), &gorm.Config{})
	require.NoError(t, err)
	model.DB = db
	t.Cleanup(func() {
		model.DB = previousDB
		sqlDB, dbErr := db.DB()
		if dbErr == nil {
			_ = sqlDB.Close()
		}
	})
	require.NoError(t, db.AutoMigrate(
		&model.Organization{},
		&model.OrganizationMember{},
		&model.OrganizationDiscountSnapshot{},
		&model.Channel{},
	))
	org := model.Organization{Id: 88, Name: "retry-org", Status: model.OrganizationStatusEnabled}
	require.NoError(t, db.Create(&org).Error)
	currentKey := "43"
	require.NoError(t, db.Create(&model.OrganizationMember{
		OrganizationId: org.Id,
		UserId:         43,
		Role:           model.OrganizationRoleMember,
		CurrentKey:     &currentKey,
	}).Error)
	firstData, err := model.MarshalOrganizationChannelDiscounts(map[int]int{99: 500_000, 100: 5_000_000})
	require.NoError(t, err)
	firstSnapshot := model.OrganizationDiscountSnapshot{
		Id:               1,
		OrganizationId:   org.Id,
		ChannelDiscounts: firstData,
	}
	require.NoError(t, db.Create(&firstSnapshot).Error)
	require.NoError(t, db.Model(&org).Update("current_discount_snapshot_id", firstSnapshot.Id).Error)

	modelName := ""
	for configuredModel := range ratio_setting.GetDefaultModelPriceMap() {
		modelName = configuredModel
		break
	}
	require.NotEmpty(t, modelName)
	gin.SetMode(gin.TestMode)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Set("group", "default")
	info := &relaycommon.RelayInfo{
		UserId:          43,
		UserGroup:       "default",
		UsingGroup:      "default",
		OriginModelName: modelName,
		ChannelMeta:     &relaycommon.ChannelMeta{ChannelId: 99},
	}

	priceData, err := ModelPriceHelperPerCall(ctx, info)
	require.NoError(t, err)
	require.NotNil(t, priceData.DiscountSnapshot)
	require.Equal(t, firstSnapshot.Id, priceData.DiscountSnapshot.SnapshotID)
	require.True(t, priceData.DiscountSnapshotLoaded)
	require.InDelta(t, 0.5, priceData.DiscountSnapshot.EffectiveRatio(), 1e-12)
	// 双额度契约：QuotaToPreConsume 未打折，Quota 应用实际渠道折扣
	require.Greater(t, priceData.QuotaToPreConsume, 0)
	expectedQuota, err := common.QuotaFromFloatStrict(float64(priceData.QuotaToPreConsume) * 0.5)
	require.NoError(t, err)
	require.Equal(t, expectedQuota, priceData.Quota)
	info.PriceData = priceData

	// 管理员保存新折扣后，跨渠道重试仍复用原请求内存映射中的倍率。
	secondData, err := model.MarshalOrganizationChannelDiscounts(map[int]int{99: 100_000, 100: 800_000})
	require.NoError(t, err)
	secondSnapshot := model.OrganizationDiscountSnapshot{
		Id:               2,
		OrganizationId:   org.Id,
		ChannelDiscounts: secondData,
	}
	require.NoError(t, db.Create(&secondSnapshot).Error)
	require.NoError(t, db.Model(&org).Update("current_discount_snapshot_id", secondSnapshot.Id).Error)

	retryInfo := &relaycommon.RelayInfo{
		UserId:          43,
		UserGroup:       "default",
		UsingGroup:      "default",
		OriginModelName: modelName,
		ChannelMeta:     &relaycommon.ChannelMeta{ChannelId: 100},
		PriceData:       info.PriceData,
	}
	retryData, err := ModelPriceHelperPerCall(ctx, retryInfo)
	require.NoError(t, err)
	require.NotNil(t, retryData.DiscountSnapshot)
	require.Equal(t, firstSnapshot.Id, retryData.DiscountSnapshot.SnapshotID, "retry must keep the request-fixed snapshot")
	require.Equal(t, 100, retryData.DiscountSnapshot.AppliedChannelId)
	require.InDelta(t, 5.0, retryData.DiscountSnapshot.EffectiveRatio(), 1e-12, "retry keeps the request snapshot ratio, not the latest config")
	retryQuota, err := common.QuotaFromFloatStrict(float64(retryData.QuotaToPreConsume) * 5.0)
	require.NoError(t, err)
	require.Equal(t, retryQuota, retryData.Quota)
}

func TestModelPriceHelperTieredUsesPreloadedRequestInput(t *testing.T) {
	gin.SetMode(gin.TestMode)

	saved := map[string]string{}
	require.NoError(t, config.GlobalConfig.SaveToDB(func(key, value string) error {
		saved[key] = value
		return nil
	}))
	t.Cleanup(func() {
		require.NoError(t, config.GlobalConfig.LoadFromDB(saved))
	})

	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		"billing_setting.billing_mode": `{"tiered-test-model":"tiered_expr"}`,
		"billing_setting.billing_expr": `{"tiered-test-model":"param(\"stream\") == true ? tier(\"stream\", p * 3) : tier(\"base\", p * 2)"}`,
	}))

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/api/channel/test/1", nil)
	req.Body = nil
	req.ContentLength = 0
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req
	ctx.Set("group", "default")

	info := &relaycommon.RelayInfo{
		OriginModelName: "tiered-test-model",
		UserGroup:       "default",
		UsingGroup:      "default",
		RequestHeaders:  map[string]string{"Content-Type": "application/json"},
		BillingRequestInput: &billingexpr.RequestInput{
			Headers: map[string]string{"Content-Type": "application/json"},
			Body:    []byte(`{"stream":true}`),
		},
	}

	priceData, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{
		BillingRatios: map[string]float64{"n": 3},
	})
	require.NoError(t, err)
	require.Equal(t, 1500, priceData.QuotaToPreConsume)
	require.NotNil(t, info.TieredBillingSnapshot)
	require.Equal(t, "stream", info.TieredBillingSnapshot.EstimatedTier)
	require.Equal(t, billing_setting.BillingModeTieredExpr, info.TieredBillingSnapshot.BillingMode)
	require.Equal(t, common.QuotaPerUnit, info.TieredBillingSnapshot.QuotaPerUnit)
}

func TestModelPriceHelperTieredPreConsumeMaxTokensFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)

	saved := map[string]string{}
	require.NoError(t, config.GlobalConfig.SaveToDB(func(key, value string) error {
		saved[key] = value
		return nil
	}))
	t.Cleanup(func() {
		require.NoError(t, config.GlobalConfig.LoadFromDB(saved))
	})

	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		"billing_setting.billing_mode":    `{"tiered-fallback-model":"tiered_expr"}`,
		"billing_setting.billing_expr":    `{"tiered-fallback-model":"tier(\"base\", p * 3 + c * 15)"}`,
		"group_ratio_setting.group_ratio": `{"default":1,"free":0}`,
	}))

	const promptTokens = 1000

	cases := []struct {
		name      string
		group     string
		maxTokens int
		expected  int
	}{
		{
			// max_tokens omitted in a paid group -> fall back to 8192 completion tokens.
			// p*3 + c*15 = 1000*3 + 8192*15 = 125880 -> /1e6 * 500000 = 62940
			name:      "non-free group falls back to 8192 completion tokens",
			group:     "default",
			maxTokens: 0,
			expected:  62940,
		},
		{
			// explicit max_tokens is used verbatim, no fallback.
			// 1000*3 + 100*15 = 4500 -> /1e6 * 500000 = 2250
			name:      "explicit max_tokens is used verbatim",
			group:     "default",
			maxTokens: 100,
			expected:  2250,
		},
		{
			// free group (ratio 0) stays zero; fallback is gated on non-zero group ratio.
			name:      "free group stays zero without fallback",
			group:     "free",
			maxTokens: 0,
			expected:  0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			req.Header.Set("Content-Type", "application/json")
			ctx.Request = req
			ctx.Set("group", tc.group)

			info := &relaycommon.RelayInfo{
				OriginModelName: "tiered-fallback-model",
				UserGroup:       tc.group,
				UsingGroup:      tc.group,
				RequestHeaders:  map[string]string{"Content-Type": "application/json"},
				BillingRequestInput: &billingexpr.RequestInput{
					Headers: map[string]string{"Content-Type": "application/json"},
					Body:    []byte(`{}`),
				},
			}

			priceData, err := ModelPriceHelper(ctx, info, promptTokens, &types.TokenCountMeta{MaxTokens: tc.maxTokens})
			require.NoError(t, err)
			require.Equal(t, tc.expected, priceData.QuotaToPreConsume)
		})
	}
}

func TestModelPriceHelperTieredRejectsPreConsumeOverflow(t *testing.T) {
	gin.SetMode(gin.TestMode)

	saved := map[string]string{}
	require.NoError(t, config.GlobalConfig.SaveToDB(func(key, value string) error {
		saved[key] = value
		return nil
	}))
	t.Cleanup(func() {
		require.NoError(t, config.GlobalConfig.LoadFromDB(saved))
	})

	require.NoError(t, config.GlobalConfig.LoadFromDB(map[string]string{
		"billing_setting.billing_mode":    `{"tiered-overflow-model":"tiered_expr"}`,
		"billing_setting.billing_expr":    `{"tiered-overflow-model":"tier(\"overflow\", p * 1000000000000000)"}`,
		"group_ratio_setting.group_ratio": `{"default":1}`,
	}))

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	ctx.Set("group", "default")
	info := &relaycommon.RelayInfo{
		OriginModelName: "tiered-overflow-model",
		UserGroup:       "default",
		UsingGroup:      "default",
		BillingRequestInput: &billingexpr.RequestInput{
			Body: []byte(`{}`),
		},
	}

	_, err := ModelPriceHelper(ctx, info, 1000, &types.TokenCountMeta{})

	var clamp *common.QuotaClamp
	require.ErrorAs(t, err, &clamp)
	require.Equal(t, "QuotaRound", clamp.Op)
	require.Equal(t, common.QuotaClampOverflow, clamp.Kind)
}

func TestModelPriceHelperRequestBillingRatiosOnlyApplyToFixedPrice(t *testing.T) {
	gin.SetMode(gin.TestMode)
	savedModelPrices := ratio_setting.ModelPrice2JSONString()
	savedModelRatios := ratio_setting.ModelRatio2JSONString()
	t.Cleanup(func() {
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(savedModelPrices))
		require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(savedModelRatios))
	})

	modelPrices, err := common.Marshal(map[string]float64{
		"fixed-image-price":      0.04,
		"fractional-image-price": 0.0000012,
		"overflow-image-price":   float64(common.MaxQuota) / common.QuotaPerUnit / 2,
	})
	require.NoError(t, err)
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(string(modelPrices)))
	modelRatios, err := common.Marshal(map[string]float64{"ratio-image-price": 15})
	require.NoError(t, err)
	require.NoError(t, ratio_setting.UpdateModelRatioByJSONString(string(modelRatios)))

	tests := []struct {
		name           string
		model          string
		wantQuota      int
		wantUsePrice   bool
		wantImageCount bool
	}{
		{
			name:           "fixed price applies image count",
			model:          "fixed-image-price",
			wantQuota:      180000,
			wantUsePrice:   true,
			wantImageCount: true,
		},
		{
			name:         "ratio price ignores request billing ratios",
			model:        "ratio-image-price",
			wantQuota:    15000,
			wantUsePrice: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Set("group", "default")
			info := &relaycommon.RelayInfo{
				OriginModelName: tt.model,
				UserGroup:       "default",
				UsingGroup:      "default",
			}
			meta := &types.TokenCountMeta{
				ImagePriceRatio: 3,
				BillingRatios:   map[string]float64{"n": 3},
			}

			priceData, err := ModelPriceHelper(ctx, info, 1000, meta)

			require.NoError(t, err)
			require.Equal(t, tt.wantQuota, priceData.QuotaToPreConsume)
			require.Equal(t, tt.wantUsePrice, priceData.UsePrice)
			require.Equal(t, tt.wantImageCount, priceData.HasOtherRatio("n"))
			require.Equal(t, priceData.OtherRatios(), info.PriceData.OtherRatios())
		})
	}

	newInfo := func(model string) (*gin.Context, *relaycommon.RelayInfo) {
		ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx.Set("group", "default")
		return ctx, &relaycommon.RelayInfo{
			OriginModelName: model,
			UserGroup:       "default",
			UsingGroup:      "default",
		}
	}
	meta := &types.TokenCountMeta{BillingRatios: map[string]float64{"n": 3}}

	ctx, info := newInfo("fractional-image-price")
	priceData, err := ModelPriceHelper(ctx, info, 0, meta)
	require.NoError(t, err)
	// 0.0000012 * 500000 * 3 = 1.8, then truncate once to 1.
	require.Equal(t, 1, priceData.QuotaToPreConsume)

	ctx, info = newInfo("overflow-image-price")
	_, err = ModelPriceHelper(ctx, info, 0, meta)
	var clamp *common.QuotaClamp
	require.ErrorAs(t, err, &clamp)
	require.Equal(t, "QuotaFromFloat", clamp.Op)
	require.Equal(t, common.QuotaClampOverflow, clamp.Kind)
	require.Nil(t, info.Billing)
}
