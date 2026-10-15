package controller

import (
	"errors"
	"net/http"

	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

func GetVantageIntegrationSettings(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	config, err := model.GetVantageIntegrationConfig()
	if err != nil {
		vantageRespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": config.Settings()})
}

func UpdateVantageIntegrationSettings(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	var body struct {
		Enabled     *bool     `json:"enabled"`
		AllowedIPv4 *[]string `json:"allowed_ipv4"`
		Secret      string    `json:"secret"`
	}
	if !vantageBindJSON(c, &body, map[string]bool{"enabled": true, "allowed_ipv4": true, "secret": true}) {
		return
	}
	if body.Enabled == nil || body.AllowedIPv4 == nil {
		vantageRejectRequest(c, "Integration enabled state and IPv4 allowlist are required.")
		return
	}
	settings, err := model.SaveVantageIntegrationSettings(*body.Enabled, *body.AllowedIPv4, body.Secret)
	if err != nil {
		var configError *model.VantageIntegrationConfigError
		if errors.As(err, &configError) {
			vantageRejectRequest(c, configError.Message)
			return
		}
		vantageRespondError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": settings})
}
