package controller

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm/clause"
)

func GetOperationsNotifications(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()
	settings, err := model.LoadOperationsSettings(ctx)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	var deliveries []model.OperationsDelivery
	if err = model.DB.WithContext(ctx).Order("created_at DESC").Order(clause.OrderByColumn{Column: clause.Column{Name: "key"}, Desc: true}).Limit(50).Find(&deliveries).Error; err != nil {
		common.ApiError(c, err)
		return
	}
	common.ApiSuccess(c, gin.H{"settings": settings, "deliveries": deliveries, "runtime": service.OperationsNotificationStatus()})
}
func UpdateOperationsNotifications(c *gin.Context) {
	var settings model.OperationsSettings
	if c.ShouldBindJSON(&settings) != nil || len(settings.Recipients) > 2048 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid notification settings"})
		return
	}
	recipients := []string{}
	seen := map[string]bool{}
	for _, value := range strings.Split(settings.Recipients, ";") {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if !common.ValidEmailDeliveryAddress(value) {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid administrator email address"})
			return
		}
		if !seen[value] {
			recipients = append(recipients, value)
			seen[value] = true
		}
	}
	if len(recipients) > 5 || ((settings.DailyEnabled || settings.EmergencyEnabled) && len(recipients) == 0) {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Provide one to five administrator email addresses"})
		return
	}
	settings.Recipients = strings.Join(recipients, ";")
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()
	if err := model.SaveOperationsSettings(ctx, settings); err != nil {
		common.ApiError(c, err)
		return
	}
	if err := service.RefreshOperationsNotifications(ctx); err != nil {
		common.ApiError(c, err)
		return
	}
	recordManageAudit(c, "operations.settings.update", map[string]any{"daily_enabled": settings.DailyEnabled, "emergency_enabled": settings.EmergencyEnabled, "preview_only": settings.PreviewOnly, "recipient_count": len(recipients)})
	common.ApiSuccess(c, settings)
}
func RetryOperationsDelivery(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()
	key := c.Param("key")
	if len(key) > 96 {
		c.Status(http.StatusBadRequest)
		return
	}
	// Explicit human retry may include an unknown DATA outcome. Accepted mail is
	// never eligible; retries are restricted to today's report period.
	day := time.Now().In(time.FixedZone("Asia/Shanghai", 8*3600)).AddDate(0, 0, -1).Format("2006-01-02")
	result := model.DB.WithContext(ctx).Model(&model.OperationsDelivery{}).Where(&model.OperationsDelivery{Key: key, Day: day}).Where("kind IN ?", []string{"account", "admin"}).Where("status IN ?", []string{"failed", "unknown", "preview"}).Updates(map[string]any{"status": "pending", "attempts": 0, "next_attempt": 0, "reason": "manual_retry", "updated_at": time.Now().Unix()})
	if result.Error != nil {
		common.ApiError(c, result.Error)
		return
	}
	if result.RowsAffected != 1 {
		c.JSON(http.StatusConflict, gin.H{"success": false, "message": "Delivery is no longer eligible for retry"})
		return
	}
	recordManageAudit(c, "operations.delivery.retry", map[string]any{"delivery_key": key})
	common.ApiSuccess(c, nil)
}
