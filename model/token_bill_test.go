package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func createTokenBillTestFixture(t *testing.T) TokenBillFilters {
	t.Helper()
	setupOrganizationTestState(t)
	require.NoError(t, DB.Create(&Organization{Id: 201, Name: "Manual Audit Org", Status: OrganizationStatusEnabled}).Error)
	require.NoError(t, DB.Create(&OrganizationMember{
		OrganizationId: 201,
		UserId:         11,
		Role:           OrganizationRoleMember,
		JoinedAt:       100,
		BillingStartAt: 100,
	}).Error)
	baseURL := "https://api.example.com/v1/"
	require.NoError(t, DB.Create(&Channel{Id: 7, Name: "Primary Channel", Key: "test", BaseURL: &baseURL}).Error)
	require.NoError(t, LOG_DB.Create(&[]Log{
		{Id: 1, UserId: 11, Username: "alice", CreatedAt: 110, Type: LogTypeConsume, Quota: 100, PromptTokens: 10, CompletionTokens: 20, ModelName: "gpt-test", ChannelId: 7, RequestId: "req-consume", UpstreamRequestId: "upstream-consume"},
		{Id: 2, UserId: 11, Username: "alice", CreatedAt: 120, Type: LogTypeRefund, Quota: 30, RequestId: "req-refund", UpstreamRequestId: "upstream-refund"},
		{Id: 3, UserId: 11, Username: "alice", CreatedAt: 130, Type: LogTypeSystem, Quota: -5, RequestId: "req-adjust"},
		{Id: 4, UserId: 11, Username: "alice", CreatedAt: 140, Type: LogTypeSystem, Quota: 0, RequestId: "req-operational"},
		{Id: 5, UserId: 12, Username: "bob", CreatedAt: 150, Type: LogTypeConsume, Quota: 50, PromptTokens: 5, ModelName: "gpt-other", RequestId: "req-other"},
		{Id: 6, UserId: 11, Username: "alice", CreatedAt: 200, Type: LogTypeConsume, Quota: 999, RequestId: "req-outside"},
	}).Error)
	return TokenBillFilters{
		StartTimestamp: 100,
		EndTimestamp:   200,
		Perspective:    TokenBillPerspectiveCustomer,
		BillType:       TokenBillTypeAll,
	}
}

func TestTokenBillUpstreamPerspectiveUsesConsumptionFactsOnly(t *testing.T) {
	filters := createTokenBillTestFixture(t)
	filters.Perspective = TokenBillPerspectiveUpstream

	summary, err := GetTokenBillSummary(filters)
	require.NoError(t, err)
	assert.EqualValues(t, TokenBillPerspectiveUpstream, summary.Perspective)
	assert.EqualValues(t, int64(150), summary.NetQuota)
	assert.EqualValues(t, int64(150), summary.ConsumeQuota)
	assert.EqualValues(t, int64(0), summary.RefundQuota)
	assert.EqualValues(t, int64(2), summary.RecordCount)
	assert.EqualValues(t, int64(15), summary.PromptTokens)
	assert.EqualValues(t, int64(20), summary.CompletionTokens)
}

func TestTokenBillUpstreamPerspectiveKeepsChannelsSeparateWithinCurrentAddress(t *testing.T) {
	filters := createTokenBillTestFixture(t)
	sharedBaseURL := "https://api.example.com/v1"
	require.NoError(t, DB.Create(&Channel{Id: 8, Name: "Secondary Channel", Key: "test", BaseURL: &sharedBaseURL}).Error)
	require.NoError(t, LOG_DB.Create(&Log{
		Id: 7, UserId: 13, Username: "carol", CreatedAt: 160, Type: LogTypeConsume,
		Quota: 25, PromptTokens: 2, CompletionTokens: 3, ModelName: "gpt-test",
		ChannelId: 8, RequestId: "req-secondary",
	}).Error)
	filters.Perspective = TokenBillPerspectiveAPI
	summary, err := GetTokenBillSummary(filters)
	require.NoError(t, err)
	assert.EqualValues(t, int64(175), summary.NetQuota)
	assert.EqualValues(t, int64(3), summary.RecordCount)

	groups, err := GetTokenBillGroups(filters, TokenBillDimensionUpstreamChannel, 1, 20)
	require.NoError(t, err)
	require.Len(t, groups.Items, 3)
	assert.EqualValues(t, int64(3), groups.Total)
	assert.EqualValues(t, "https://api.example.com/v1", groups.Items[0].APIAddress)
	assert.EqualValues(t, 7, groups.Items[0].ChannelId)
	assert.EqualValues(t, int64(100), groups.Items[0].Quota)
	assert.EqualValues(t, 0, groups.Items[1].ChannelId)
	assert.Empty(t, groups.Items[1].APIAddress)
	assert.EqualValues(t, "https://api.example.com/v1", groups.Items[2].APIAddress)
	assert.EqualValues(t, 8, groups.Items[2].ChannelId)
	assert.EqualValues(t, int64(25), groups.Items[2].Quota)

	modelGroups, err := GetTokenBillGroups(filters, TokenBillDimensionUpstreamModel, 1, 20)
	require.NoError(t, err)
	require.Len(t, modelGroups.Items, 3)
	assert.EqualValues(t, int64(3), modelGroups.Total)
	assert.EqualValues(t, "https://api.example.com/v1", modelGroups.Items[0].APIAddress)
	assert.EqualValues(t, 7, modelGroups.Items[0].ChannelId)
	assert.EqualValues(t, "gpt-test", modelGroups.Items[0].ModelName)
	assert.EqualValues(t, "https://api.example.com/v1", modelGroups.Items[2].APIAddress)
	assert.EqualValues(t, 8, modelGroups.Items[2].ChannelId)
	assert.EqualValues(t, "gpt-test", modelGroups.Items[2].ModelName)

	filters.APIAddress = groups.Items[0].APIAddress
	filters.APIAddressSet = true
	filters.ChannelId = groups.Items[0].ChannelId
	filters.ChannelIdSet = true
	filters.ModelName = modelGroups.Items[0].ModelName
	filters.ModelNameSet = true
	page, err := GetTokenBillEntries(filters, 1, 20)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.EqualValues(t, "https://api.example.com/v1", page.Items[0].ChannelAPIAddress)
	assert.EqualValues(t, 7, page.Items[0].ChannelId)

	filters.APIAddress = TokenBillUnknownAPIAddress
	filters.ChannelId = 0
	filters.ModelName = ""
	filters.ModelNameSet = false
	page, err = GetTokenBillEntries(filters, 1, 20)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.EqualValues(t, "req-other", page.Items[0].RequestId)
	assert.Empty(t, page.Items[0].ChannelAPIAddress)
}

func TestTokenBillUpstreamModelPreservesAddressChannelAndModel(t *testing.T) {
	filters := createTokenBillTestFixture(t)
	require.NoError(t, LOG_DB.Create(&Log{
		Id: 7, UserId: 11, Username: "alice", CreatedAt: 160, Type: LogTypeConsume,
		Quota: 40, PromptTokens: 4, CompletionTokens: 6, ModelName: "image-test",
		ChannelId: 7, RequestId: "req-image",
	}).Error)
	filters.Perspective = TokenBillPerspectiveAPI

	groups, err := GetTokenBillGroups(filters, TokenBillDimensionUpstreamModel, 1, 20)
	require.NoError(t, err)
	assert.EqualValues(t, int64(3), groups.Total)
	assert.Contains(t, groups.Items, TokenBillGroupRow{
		Key: "7\x1fhttps://api.example.com/v1\x1fgpt-test", Label: "https://api.example.com/v1",
		ChannelId: 7, ChannelName: "Primary Channel", ModelName: "gpt-test", APIAddress: "https://api.example.com/v1",
		RecordCount: 1, PromptTokens: 10, CompletionTokens: 20, Quota: 100,
	})
	assert.Contains(t, groups.Items, TokenBillGroupRow{
		Key: "7\x1fhttps://api.example.com/v1\x1fimage-test", Label: "https://api.example.com/v1",
		ChannelId: 7, ChannelName: "Primary Channel", ModelName: "image-test", APIAddress: "https://api.example.com/v1",
		RecordCount: 1, PromptTokens: 4, CompletionTokens: 6, Quota: 40,
	})

	filters.APIAddress = "https://api.example.com/v1"
	filters.APIAddressSet = true
	filters.ChannelId = 7
	filters.ChannelIdSet = true
	filters.ModelName = "image-test"
	filters.ModelNameSet = true
	page, err := GetTokenBillEntries(filters, 1, 20)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.EqualValues(t, "req-image", page.Items[0].RequestId)
}

func TestTokenBillCompositeGroupsPreserveCustomerAndChannelSubjects(t *testing.T) {
	filters := createTokenBillTestFixture(t)
	secondBaseURL := "https://second.example.com/v1"
	require.NoError(t, DB.Create(&Channel{Id: 8, Name: "Secondary Channel", Key: "test", BaseURL: &secondBaseURL}).Error)
	require.NoError(t, LOG_DB.Create(&[]Log{
		{Id: 7, UserId: 12, Username: "bob", CreatedAt: 160, Type: LogTypeConsume, Quota: 40, ModelName: "gpt-test", ChannelId: 7, RequestId: "req-bob-primary"},
		{Id: 8, UserId: 12, Username: "bob", CreatedAt: 170, Type: LogTypeConsume, Quota: 25, ModelName: "gpt-test", ChannelId: 8, RequestId: "req-bob-secondary"},
	}).Error)

	customerModels, err := GetTokenBillGroups(filters, TokenBillDimensionUserModel, 1, 20)
	require.NoError(t, err)
	assert.EqualValues(t, int64(4), customerModels.Total)
	assert.Contains(t, customerModels.Items, TokenBillGroupRow{
		Key: "11\x1fgpt-test", Label: "alice", UserId: 11, Username: "alice", ModelName: "gpt-test",
		RecordCount: 1, PromptTokens: 10, CompletionTokens: 20, Quota: 100,
	})
	assert.Contains(t, customerModels.Items, TokenBillGroupRow{
		Key: "12\x1fgpt-test", Label: "bob", UserId: 12, Username: "bob", ModelName: "gpt-test",
		RecordCount: 2, Quota: 65,
	})

	customerChannels, err := GetTokenBillGroups(filters, TokenBillDimensionUserChannel, 1, 20)
	require.NoError(t, err)
	assert.EqualValues(t, int64(5), customerChannels.Total)

	filters.Perspective = TokenBillPerspectiveUpstream
	channelModels, err := GetTokenBillGroups(filters, TokenBillDimensionChannelModel, 1, 20)
	require.NoError(t, err)
	assert.EqualValues(t, int64(3), channelModels.Total)
	assert.Contains(t, channelModels.Items, TokenBillGroupRow{
		Key: "7\x1fgpt-test", Label: "Primary Channel", ChannelId: 7, ChannelName: "Primary Channel", ModelName: "gpt-test",
		RecordCount: 2, PromptTokens: 10, CompletionTokens: 20, Quota: 140,
	})
	assert.Contains(t, channelModels.Items, TokenBillGroupRow{
		Key: "8\x1fgpt-test", Label: "Secondary Channel", ChannelId: 8, ChannelName: "Secondary Channel", ModelName: "gpt-test",
		RecordCount: 1, Quota: 25,
	})
}

func TestTokenBillGroupsProvideOverviewBeforeDetails(t *testing.T) {
	filters := createTokenBillTestFixture(t)

	customers, err := GetTokenBillGroups(filters, TokenBillDimensionUser, 1, 20)
	require.NoError(t, err)
	require.Len(t, customers.Items, 2)
	assert.EqualValues(t, int64(2), customers.Total)
	assert.EqualValues(t, 11, customers.Items[0].UserId)
	assert.EqualValues(t, "alice", customers.Items[0].Label)
	assert.EqualValues(t, int64(70), customers.Items[0].Quota)

	filters.Perspective = TokenBillPerspectiveUpstream
	channels, err := GetTokenBillGroups(filters, TokenBillDimensionChannel, 1, 20)
	require.NoError(t, err)
	require.Len(t, channels.Items, 2)
	assert.EqualValues(t, int64(2), channels.Total)
	assert.EqualValues(t, 7, channels.Items[0].ChannelId)
	assert.EqualValues(t, "Primary Channel", channels.Items[0].Label)
	assert.EqualValues(t, int64(100), channels.Items[0].Quota)

	models, err := GetTokenBillGroups(filters, TokenBillDimensionChannelModel, 1, 20)
	require.NoError(t, err)
	require.Len(t, models.Items, 2)
	assert.EqualValues(t, "gpt-test", models.Items[0].ModelName)
	assert.EqualValues(t, 7, models.Items[0].ChannelId)
	assert.EqualValues(t, int64(100), models.Items[0].Quota)
}

func TestTokenBillDetailsCanFilterUnattributedGroups(t *testing.T) {
	filters := createTokenBillTestFixture(t)
	filters.Perspective = TokenBillPerspectiveUpstream
	filters.ChannelId = 0
	filters.ChannelIdSet = true

	page, err := GetTokenBillEntries(filters, 1, 20)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.EqualValues(t, "req-other", page.Items[0].RequestId)

	filters.ChannelIdSet = false
	filters.Perspective = TokenBillPerspectiveCustomer
	filters.ModelName = ""
	filters.ModelNameSet = true
	page, err = GetTokenBillEntries(filters, 1, 20)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.EqualValues(t, "req-refund", page.Items[0].RequestId)
}

func TestTokenBillSummaryUsesOnlyExistingBillingFacts(t *testing.T) {
	filters := createTokenBillTestFixture(t)

	summary, err := GetTokenBillSummary(filters)
	require.NoError(t, err)
	assert.EqualValues(t, int64(120), summary.NetQuota)
	assert.EqualValues(t, int64(150), summary.ConsumeQuota)
	assert.EqualValues(t, int64(-30), summary.RefundQuota)
	assert.EqualValues(t, int64(3), summary.RecordCount)
	assert.EqualValues(t, int64(15), summary.PromptTokens)
	assert.EqualValues(t, int64(20), summary.CompletionTokens)
	var consumeQuota int64
	require.NoError(t, LOG_DB.Model(&Log{}).
		Where("created_at >= ? AND created_at < ?", 100, 200).
		Where("type = ?", LogTypeConsume).
		Select("COALESCE(sum(quota), 0)").Scan(&consumeQuota).Error)
	var refundQuota int64
	require.NoError(t, LOG_DB.Model(&Log{}).
		Where("created_at >= ? AND created_at < ?", 100, 200).
		Where("type = ?", LogTypeRefund).
		Select("COALESCE(sum(quota), 0)").Scan(&refundQuota).Error)
	assert.EqualValues(t, consumeQuota-refundQuota, summary.NetQuota)
	assert.EqualValues(t, []TokenBillFilterOption{
		{Value: "gpt-other", Label: "gpt-other"},
		{Value: "gpt-test", Label: "gpt-test"},
	}, summary.FilterOptions.Models)
	assert.EqualValues(t, []TokenBillFilterOption{
		{Value: 7, Label: "Primary Channel"},
	}, summary.FilterOptions.Channels)
}

func TestTokenBillOrganizationFilterUsesMembershipTime(t *testing.T) {
	filters := createTokenBillTestFixture(t)
	filters.OrganizationId = 201

	summary, err := GetTokenBillSummary(filters)
	require.NoError(t, err)
	assert.EqualValues(t, int64(70), summary.NetQuota)
	assert.EqualValues(t, int64(2), summary.RecordCount)

	page, err := GetTokenBillEntries(filters, 1, 20)
	require.NoError(t, err)
	require.Len(t, page.Items, 2)
	assert.EqualValues(t, int64(2), page.Total)
	assert.EqualValues(t, "req-refund", page.Items[0].RequestId)
	assert.EqualValues(t, -30, page.Items[0].Quota)
	assert.EqualValues(t, "Manual Audit Org", page.Items[0].OrganizationName)
	assert.EqualValues(t, "Primary Channel", page.Items[1].ChannelName)
	assert.EqualValues(t, "upstream-consume", page.Items[1].UpstreamRequestId)
}

func TestTokenBillRequestFilterMatchesLocalOrUpstreamIdentifier(t *testing.T) {
	filters := createTokenBillTestFixture(t)
	filters.RequestId = "upstream-refund"

	page, err := GetTokenBillEntries(filters, 1, 20)
	require.NoError(t, err)
	require.Len(t, page.Items, 1)
	assert.EqualValues(t, "req-refund", page.Items[0].RequestId)
	assert.EqualValues(t, "upstream-refund", page.Items[0].UpstreamRequestId)
}

func TestTokenBillEntryPaginationIsStable(t *testing.T) {
	filters := createTokenBillTestFixture(t)

	first, err := GetTokenBillEntries(filters, 1, 2)
	require.NoError(t, err)
	second, err := GetTokenBillEntries(filters, 2, 2)
	require.NoError(t, err)

	require.Len(t, first.Items, 2)
	require.Len(t, second.Items, 1)
	assert.EqualValues(t, int64(3), first.Total)
	assert.EqualValues(t, "req-other", first.Items[0].RequestId)
	assert.EqualValues(t, "req-refund", first.Items[1].RequestId)
	assert.EqualValues(t, "req-consume", second.Items[0].RequestId)
}
