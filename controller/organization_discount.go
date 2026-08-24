package controller

import (
	"errors"
	"net/http"
	"sort"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type organizationDiscountUpdateRequest struct {
	ExpectedSnapshotId *int                                     `json:"expected_snapshot_id"`
	ChannelDiscounts   []organizationDiscountChannelItemRequest `json:"channel_discounts"`
}

type organizationDiscountChannelItemRequest struct {
	ChannelId int    `json:"channel_id"`
	Ratio     string `json:"ratio"`
}

// AdminGetOrganizationDiscount 返回组织当前折扣配置。无快照时返回
// snapshot_id 0 和空折扣集合。仅 Root 可访问（路由层 RootAuth）。
func AdminGetOrganizationDiscount(c *gin.Context) {
	organizationId, ok := parseOrganizationDiscountId(c)
	if !ok {
		return
	}
	snapshot, err := model.GetCurrentOrganizationDiscountSnapshot(organizationId)
	if err != nil {
		respondOrganizationDiscountError(c, http.StatusInternalServerError, err.Error())
		return
	}
	var discounts map[int]int
	if snapshot != nil {
		discounts, err = model.UnmarshalOrganizationChannelDiscounts(snapshot.ChannelDiscounts)
		if err != nil {
			// 损坏快照失败关闭：显式报错而不是返回 200 空配置，
			// 避免诱导 Root 用旧 snapshot_id 覆盖损坏事实。
			respondOrganizationDiscountError(c, http.StatusInternalServerError, err.Error())
			return
		}
	}
	common.ApiSuccess(c, buildOrganizationDiscountResponse(snapshot, discounts))
}

// AdminGetOrganizationDiscountChannelOptions 返回折扣配置可用的系统渠道选项。
// 渠道来自全局渠道表而非账单聚合，未产生消费的新渠道同样可配置折扣。
func AdminGetOrganizationDiscountChannelOptions(c *gin.Context) {
	if _, ok := parseOrganizationDiscountId(c); !ok {
		return
	}
	options, err := model.ListOrganizationDiscountChannelOptions()
	if err != nil {
		respondOrganizationDiscountError(c, http.StatusInternalServerError, err.Error())
		return
	}
	common.ApiSuccess(c, options)
}

// AdminUpdateOrganizationDiscount 以完整替换语义保存组织折扣：
// 一次保存、原子生效；空集合用于清除全部组织折扣。
func AdminUpdateOrganizationDiscount(c *gin.Context) {
	organizationId, ok := parseOrganizationDiscountId(c)
	if !ok {
		return
	}
	var req organizationDiscountUpdateRequest
	if err := common.DecodeJson(c.Request.Body, &req); err != nil {
		respondOrganizationDiscountError(c, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.ExpectedSnapshotId == nil || *req.ExpectedSnapshotId < 0 {
		respondOrganizationDiscountError(c, http.StatusBadRequest, "expected_snapshot_id is required and must be non-negative")
		return
	}

	params := model.SaveOrganizationDiscountParams{
		OrganizationId:     organizationId,
		ExpectedSnapshotId: *req.ExpectedSnapshotId,
		CreatedBy:          c.GetInt("id"),
	}
	for _, item := range req.ChannelDiscounts {
		scaled, err := model.ParseOrganizationDiscountRatio(item.Ratio)
		if err != nil {
			respondOrganizationDiscountError(c, http.StatusBadRequest, err.Error())
			return
		}
		params.ChannelDiscounts = append(params.ChannelDiscounts, model.OrganizationChannelDiscountParam{
			ChannelId:   item.ChannelId,
			RatioScaled: scaled,
		})
	}

	snapshot, err := model.SaveOrganizationDiscount(params)
	if err != nil {
		respondOrganizationDiscountSaveError(c, err)
		return
	}
	discounts, err := model.UnmarshalOrganizationChannelDiscounts(snapshot.ChannelDiscounts)
	if err != nil {
		respondOrganizationDiscountError(c, http.StatusInternalServerError, err.Error())
		return
	}
	common.ApiSuccess(c, buildOrganizationDiscountResponse(snapshot, discounts))
}

// AdminGetOrganizationDiscountHistory 分页返回组织折扣历史快照及派生变更。
// 快照不可编辑、删除或恢复；变更通过与同组织上一份快照比较派生。
func AdminGetOrganizationDiscountHistory(c *gin.Context) {
	organizationId, ok := parseOrganizationDiscountId(c)
	if !ok {
		return
	}
	page, pageSize, ok := parseOrganizationDiscountPagination(c)
	if !ok {
		return
	}
	history, err := model.GetOrganizationDiscountHistory(organizationId, (page-1)*pageSize, pageSize)
	if err != nil {
		// 查询失败与损坏快照同为服务端错误，不得以 200 伪装成功。
		respondOrganizationDiscountError(c, http.StatusInternalServerError, err.Error())
		return
	}
	items := make([]gin.H, 0, len(history.Items))
	for _, item := range history.Items {
		changes := make([]gin.H, 0, len(item.Changes))
		for _, change := range item.Changes {
			changes = append(changes, gin.H{
				"channel_id": change.ChannelId,
				"old_ratio":  formatOptionalOrganizationDiscountRatio(change.OldScaled),
				"new_ratio":  formatOptionalOrganizationDiscountRatio(change.NewScaled),
			})
		}
		items = append(items, gin.H{
			"snapshot_id":       item.Snapshot.Id,
			"channel_discounts": sortedChannelDiscounts(item.Discounts),
			"changes":           changes,
			"created_by":        item.Snapshot.CreatedBy,
			"created_by_name":   item.CreatedByName,
			"created_at":        item.Snapshot.CreatedAt,
		})
	}
	common.ApiSuccess(c, gin.H{
		"page":      page,
		"page_size": pageSize,
		"total":     history.Total,
		"items":     items,
	})
}

func parseOrganizationDiscountId(c *gin.Context) (int, bool) {
	organizationId, err := strconv.Atoi(c.Param("id"))
	if err != nil || organizationId <= 0 {
		respondOrganizationDiscountError(c, http.StatusBadRequest, "invalid organization id")
		return 0, false
	}
	if _, err := model.GetOrganizationById(organizationId); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			respondOrganizationDiscountError(c, http.StatusNotFound, "organization not found")
		} else {
			respondOrganizationDiscountError(c, http.StatusInternalServerError, err.Error())
		}
		return 0, false
	}
	return organizationId, true
}

func parseOrganizationDiscountPagination(c *gin.Context) (int, int, bool) {
	page, err := strconv.Atoi(c.DefaultQuery("p", "1"))
	if err != nil || page < 1 {
		respondOrganizationDiscountError(c, http.StatusBadRequest, "invalid page")
		return 0, 0, false
	}
	pageSize, err := strconv.Atoi(c.DefaultQuery("page_size", "10"))
	if err != nil || pageSize < 1 || pageSize > 100 {
		respondOrganizationDiscountError(c, http.StatusBadRequest, "invalid page size")
		return 0, 0, false
	}
	return page, pageSize, true
}

func respondOrganizationDiscountSaveError(c *gin.Context, err error) {
	switch {
	case errors.Is(err, model.ErrOrganizationDiscountSnapshotConflict):
		respondOrganizationDiscountError(c, http.StatusConflict, err.Error())
	case errors.Is(err, model.ErrOrganizationDiscountOrganizationNotFound):
		respondOrganizationDiscountError(c, http.StatusNotFound, err.Error())
	case errors.Is(err, model.ErrOrganizationDiscountInvalidRatio),
		errors.Is(err, model.ErrOrganizationDiscountInvalidPrecision),
		errors.Is(err, model.ErrOrganizationDiscountDuplicateChannel),
		errors.Is(err, model.ErrOrganizationDiscountInvalidChannel),
		errors.Is(err, model.ErrOrganizationDiscountJSONTooLarge):
		respondOrganizationDiscountError(c, http.StatusBadRequest, err.Error())
	default:
		// 数据库等基础设施错误不得伪装成客户端错误。
		respondOrganizationDiscountError(c, http.StatusInternalServerError, err.Error())
	}
}

func respondOrganizationDiscountError(c *gin.Context, status int, message string) {
	c.JSON(status, gin.H{
		"success": false,
		"message": message,
	})
}

// sortedChannelDiscounts 按渠道 ID 升序构造折扣列表，保证响应顺序稳定。
func sortedChannelDiscounts(discounts map[int]int) []gin.H {
	channelIds := make([]int, 0, len(discounts))
	for channelId := range discounts {
		channelIds = append(channelIds, channelId)
	}
	sort.Ints(channelIds)
	channelDiscounts := make([]gin.H, 0, len(discounts))
	for _, channelId := range channelIds {
		channelDiscounts = append(channelDiscounts, gin.H{
			"channel_id": channelId,
			"ratio":      model.FormatOrganizationDiscountRatio(discounts[channelId]),
		})
	}
	return channelDiscounts
}

func buildOrganizationDiscountResponse(snapshot *model.OrganizationDiscountSnapshot, discounts map[int]int) gin.H {
	if snapshot == nil {
		return gin.H{
			"snapshot_id":       0,
			"channel_discounts": []gin.H{},
		}
	}
	return gin.H{
		"snapshot_id":       snapshot.Id,
		"channel_discounts": sortedChannelDiscounts(discounts),
		"created_by":        snapshot.CreatedBy,
		"created_at":        snapshot.CreatedAt,
	}
}

func formatOptionalOrganizationDiscountRatio(scaled int) string {
	if scaled == 0 {
		return ""
	}
	return model.FormatOrganizationDiscountRatio(scaled)
}
