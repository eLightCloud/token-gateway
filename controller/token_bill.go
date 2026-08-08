package controller

import (
	"encoding/csv"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

const (
	maxTokenBillRangeSeconds = int64(92 * 24 * time.Hour / time.Second)
	maxTokenBillPageSize     = 100
	maxTokenBillOffset       = 1_000_000
)

func tokenBillFiltersFromQuery(c *gin.Context) (model.TokenBillFilters, bool) {
	startTimestamp, startErr := strconv.ParseInt(c.Query("start_timestamp"), 10, 64)
	endTimestamp, endErr := strconv.ParseInt(c.Query("end_timestamp"), 10, 64)
	if startErr != nil || endErr != nil || startTimestamp < 0 || endTimestamp <= startTimestamp {
		tokenBillBadRequest(c, "invalid billing time range")
		return model.TokenBillFilters{}, false
	}
	if endTimestamp-startTimestamp > maxTokenBillRangeSeconds {
		tokenBillBadRequest(c, "billing time range cannot exceed 92 days")
		return model.TokenBillFilters{}, false
	}
	billType := strings.TrimSpace(c.Query("type"))
	if billType == "" {
		billType = model.TokenBillTypeAll
	}
	if !model.ValidateTokenBillType(billType) {
		tokenBillBadRequest(c, "invalid bill type")
		return model.TokenBillFilters{}, false
	}
	perspective := strings.TrimSpace(c.Query("perspective"))
	if perspective == "" {
		perspective = model.TokenBillPerspectiveCustomer
	}
	if !model.ValidateTokenBillPerspective(perspective) {
		tokenBillBadRequest(c, "invalid bill perspective")
		return model.TokenBillFilters{}, false
	}
	organizationId, ok := tokenBillNonnegativeIntQuery(c, "organization_id")
	if !ok {
		return model.TokenBillFilters{}, false
	}
	channelId, ok := tokenBillNonnegativeIntQuery(c, "channel_id")
	if !ok {
		return model.TokenBillFilters{}, false
	}
	userId, ok := tokenBillNonnegativeIntQuery(c, "user_id")
	if !ok {
		return model.TokenBillFilters{}, false
	}
	_, channelIdSet := c.GetQuery("channel_id")
	modelNameValue, modelNameSet := c.GetQuery("model_name")
	modelName := strings.TrimSpace(modelNameValue)
	apiAddressValue, apiAddressSet := c.GetQuery("api_address")
	apiAddress := strings.TrimSpace(apiAddressValue)
	requestId := strings.TrimSpace(c.Query("request_id"))
	if len(modelName) > 128 || len(requestId) > 128 || len(apiAddress) > 2048 {
		tokenBillBadRequest(c, "billing filter is too long")
		return model.TokenBillFilters{}, false
	}
	return model.TokenBillFilters{
		StartTimestamp: startTimestamp,
		EndTimestamp:   endTimestamp,
		Perspective:    perspective,
		BillType:       billType,
		OrganizationId: organizationId,
		UserId:         userId,
		ModelName:      modelName,
		ModelNameSet:   modelNameSet,
		ChannelId:      channelId,
		ChannelIdSet:   channelIdSet,
		APIAddress:     apiAddress,
		APIAddressSet:  apiAddressSet,
		RequestId:      requestId,
	}, true
}

func tokenBillDimensionFromQuery(c *gin.Context, perspective string) (string, bool) {
	dimension := strings.TrimSpace(c.Query("dimension"))
	if dimension == "" {
		if perspective == model.TokenBillPerspectiveUpstream {
			dimension = model.TokenBillDimensionChannel
		} else if perspective == model.TokenBillPerspectiveAPI {
			dimension = model.TokenBillDimensionUpstreamChannel
		} else {
			dimension = model.TokenBillDimensionUser
		}
	}
	if !model.ValidateTokenBillDimension(perspective, dimension) {
		tokenBillBadRequest(c, "invalid bill dimension")
		return "", false
	}
	return dimension, true
}

func tokenBillNonnegativeIntQuery(c *gin.Context, key string) (int, bool) {
	value := strings.TrimSpace(c.Query(key))
	if value == "" {
		return 0, true
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 0 {
		tokenBillBadRequest(c, "invalid "+key)
		return 0, false
	}
	return parsed, true
}

func tokenBillPageFromQuery(c *gin.Context) (int, int, bool) {
	page := 1
	pageSize := 20
	var err error
	if value := c.Query("p"); value != "" {
		page, err = strconv.Atoi(value)
		if err != nil || page < 1 {
			tokenBillBadRequest(c, "invalid page")
			return 0, 0, false
		}
	}
	if value := c.Query("page_size"); value != "" {
		pageSize, err = strconv.Atoi(value)
		if err != nil || pageSize < 1 || pageSize > maxTokenBillPageSize {
			tokenBillBadRequest(c, "invalid page size")
			return 0, 0, false
		}
	}
	if page > maxTokenBillOffset/pageSize+1 {
		tokenBillBadRequest(c, "billing page is too deep; narrow the filters or export CSV")
		return 0, 0, false
	}
	return page, pageSize, true
}

func tokenBillBadRequest(c *gin.Context, message string) {
	c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
		"success": false,
		"message": message,
	})
}

func GetTokenBillSummary(c *gin.Context) {
	filters, ok := tokenBillFiltersFromQuery(c)
	if !ok {
		return
	}
	summary, err := service.GetTokenBillSummary(filters)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, summary)
}

func GetTokenBillEntries(c *gin.Context) {
	filters, ok := tokenBillFiltersFromQuery(c)
	if !ok {
		return
	}
	page, pageSize, ok := tokenBillPageFromQuery(c)
	if !ok {
		return
	}
	entries, err := service.GetTokenBillEntries(filters, page, pageSize)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, entries)
}

func GetTokenBillGroups(c *gin.Context) {
	filters, ok := tokenBillFiltersFromQuery(c)
	if !ok {
		return
	}
	dimension, ok := tokenBillDimensionFromQuery(c, filters.Perspective)
	if !ok {
		return
	}
	page, pageSize, ok := tokenBillPageFromQuery(c)
	if !ok {
		return
	}
	groups, err := service.GetTokenBillGroups(filters, dimension, page, pageSize)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, groups)
}

func ExportTokenBillCSV(c *gin.Context) {
	filters, ok := tokenBillFiltersFromQuery(c)
	if !ok {
		return
	}
	filename := fmt.Sprintf(
		"token-bill-%s-%s-%s.csv",
		filters.Perspective,
		time.Unix(filters.StartTimestamp, 0).Format("20060102"),
		time.Unix(filters.EndTimestamp-1, 0).Format("20060102"),
	)
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
	c.Status(http.StatusOK)
	_, _ = c.Writer.Write([]byte{0xEF, 0xBB, 0xBF})
	writer := csv.NewWriter(c.Writer)
	if err := service.WriteTokenBillCSV(writer, filters); err != nil {
		// Headers may already be committed, so only record the stream error.
		_ = c.Error(err)
	}
}
