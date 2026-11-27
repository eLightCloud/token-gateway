package controller

import (
	"context"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func operationsDeliveriesQuery(ctx context.Context) *gorm.DB {
	return model.DB.WithContext(ctx).
		Order("created_at DESC").
		Order(clause.OrderByColumn{Column: clause.Column{Name: "key"}, Desc: true})
}

func getOperationsDeliveryPage(c *gin.Context, ctx context.Context) {
	page := common.GetPageQuery(c)
	page.Page = max(1, page.Page)
	page.PageSize = max(1, page.PageSize)

	var total int64
	if err := model.DB.WithContext(ctx).Model(&model.OperationsDelivery{}).Count(&total).Error; err != nil {
		common.ApiError(c, err)
		return
	}
	deliveries := make([]model.OperationsDelivery, 0)
	if total > 0 && int64(page.Page-1) <= (total-1)/int64(page.PageSize) {
		if err := operationsDeliveriesQuery(ctx).
			Offset(page.GetStartIdx()).Limit(page.GetPageSize()).Find(&deliveries).Error; err != nil {
			common.ApiError(c, err)
			return
		}
	}
	page.SetTotal(int(total))
	page.SetItems(deliveries)
	common.ApiSuccess(c, page)
}
