package controller

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"net/http"
)

func GetSeedanceEnhanceSettings(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	settings, err := model.LoadSeedanceEnhanceSettings(0)
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "Video settings are unavailable; verify the server encryption key"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": settings.Public()})
}

func UpdateSeedanceEnhanceSettings(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	var update model.SeedanceEnhanceSettings
	if err := common.DecodeJson(http.MaxBytesReader(c.Writer, c.Request.Body, 32<<10), &update); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid video settings"})
		return
	}
	saved, err := model.SaveSeedanceEnhanceSettings(update)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	model.InitChannelCache()
	recordManageAudit(c, "video.enhance.settings.update", map[string]any{"version": saved.Version, "enabled": saved.Enabled})
	c.JSON(http.StatusOK, gin.H{"success": true, "data": saved})
}

func TestSeedanceEnhanceStorage(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	err := service.TestSeedanceEnhanceStorage(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "message": "OSS write, read and delete succeeded; paid enhancement was not submitted"})
}

func GetSeedanceEnhanceBilling(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	query := model.DB.Where("task_id = ?", c.Param("task_id"))
	admin := c.GetInt("role") >= common.RoleAdminUser
	if !admin {
		query = query.Where("user_id = ?", c.GetInt("id"))
	}
	var task model.Task
	if err := query.First(&task).Error; err != nil || !service.IsSeedanceEnhanceTask(&task) {
		c.JSON(http.StatusNotFound, gin.H{"success": false, "message": "Task not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": service.SeedanceEnhanceBillingSummary(&task, admin, c.GetInt("role") == common.RoleRootUser)})
}
