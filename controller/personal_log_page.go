package controller

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
)

func GetPersonalLogPage(c *gin.Context) {
	if !model.PersonalLogCursorEnabled() {
		common.ApiErrorT(c, "Personal log pagination is unavailable")
		return
	}
	filter := logFilterFromQuery(c, false)
	if filter.StartTimestamp < 0 || filter.EndTimestamp < 0 ||
		(filter.EndTimestamp != 0 && filter.StartTimestamp > filter.EndTimestamp) {
		common.ApiErrorT(c, "Invalid parameters")
		return
	}
	var maxRange int64
	if c.GetInt("role") < common.RoleAdminUser {
		maxRange = operation_setting.GetLogSetting().MaxRangeSeconds()
		if filter.StartTimestamp != 0 {
			if _, err := limitLogTimeRange(filter.StartTimestamp, filter.EndTimestamp); err != nil {
				common.ApiError(c, err)
				return
			}
		}
	}
	heavy := filter.RequestId == "" && filter.UpstreamRequestId == "" && filter != (model.LogFilter{})
	ctx, release, ok := beginLogRead(c, heavy, logLatestReadTimeout)
	if !ok {
		return
	}
	defer release()
	userID := c.GetInt("id")
	page, err := model.ReadPersonalLogPage(ctx, userID, filter, common.GetPageQuery(c).GetPageSize(), maxRange, c.Query("cursor"))
	if err != nil {
		if _, ok := err.(*common.Message); !ok {
			common.SysError(common.LogText("failed to search user logs: %s", err.Error()))
			err = common.NewMessage("Failed to load logs")
		}
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{
		"items": projectLogsForReseller(page.ResellerKind, page.Logs), "record_keys": page.RecordKeys,
		"next_cursor": page.NextCursor, "has_more": page.NextCursor != "",
	})
}
