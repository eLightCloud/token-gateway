package controller

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func tokenBillTestContext(target string) *gin.Context {
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest("GET", target, nil)
	return context
}

func TestTokenBillFiltersKeepPerspectiveAndExplicitUnknownDimensions(t *testing.T) {
	c := tokenBillTestContext("/?start_timestamp=100&end_timestamp=200&perspective=upstream&channel_id=0&model_name=")

	filters, ok := tokenBillFiltersFromQuery(c)
	require.True(t, ok)
	assert.Equal(t, model.TokenBillPerspectiveUpstream, filters.Perspective)
	assert.True(t, filters.ChannelIdSet)
	assert.True(t, filters.ModelNameSet)
	assert.Zero(t, filters.ChannelId)
	assert.Empty(t, filters.ModelName)
}

func TestTokenBillDimensionRejectsCustomerOnlyGroupingForUpstream(t *testing.T) {
	c := tokenBillTestContext("/?dimension=user")

	_, ok := tokenBillDimensionFromQuery(c, model.TokenBillPerspectiveUpstream)
	assert.False(t, ok)
	assert.True(t, c.IsAborted())
}

func TestTokenBillAPIAddressPerspectiveDefaultsToUpstreamChannelDimension(t *testing.T) {
	c := tokenBillTestContext("/?start_timestamp=100&end_timestamp=200&perspective=api_address&api_address=https%3A%2F%2Fapi.example.com%2Fv1")

	filters, ok := tokenBillFiltersFromQuery(c)
	require.True(t, ok)
	assert.Equal(t, model.TokenBillPerspectiveAPI, filters.Perspective)
	assert.True(t, filters.APIAddressSet)
	assert.Equal(t, "https://api.example.com/v1", filters.APIAddress)

	dimension, ok := tokenBillDimensionFromQuery(c, filters.Perspective)
	require.True(t, ok)
	assert.Equal(t, model.TokenBillDimensionUpstreamChannel, dimension)
}

func TestTokenBillAPIAddressPerspectiveAcceptsUpstreamModelDimension(t *testing.T) {
	c := tokenBillTestContext("/?dimension=upstream_channel_model")

	dimension, ok := tokenBillDimensionFromQuery(c, model.TokenBillPerspectiveAPI)
	require.True(t, ok)
	assert.Equal(t, model.TokenBillDimensionUpstreamModel, dimension)
}

func TestTokenBillFiltersAcceptStoredUpstreamRequestIDLength(t *testing.T) {
	requestId := strings.Repeat("u", 128)
	c := tokenBillTestContext("/?start_timestamp=100&end_timestamp=200&request_id=" + requestId)

	filters, ok := tokenBillFiltersFromQuery(c)
	require.True(t, ok)
	assert.Equal(t, requestId, filters.RequestId)
}

func TestExportTokenBillCSVRejectsInvalidBillingConfigurationBeforeCSVHeaders(t *testing.T) {
	originalQuotaPerUnit := common.QuotaPerUnit
	t.Cleanup(func() { common.QuotaPerUnit = originalQuotaPerUnit })
	common.QuotaPerUnit = 0

	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest("GET", "/?start_timestamp=100&end_timestamp=200", nil)

	ExportTokenBillCSV(c)

	assert.Contains(t, recorder.Header().Get("Content-Type"), "application/json")
	assert.Empty(t, recorder.Header().Get("Content-Disposition"))
	assert.JSONEq(t, `{"success":false,"message":"QuotaPerUnit must be a finite number greater than 0"}`, recorder.Body.String())
}
