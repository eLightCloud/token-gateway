package controller

import (
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"strconv"
)

func GetResellerDiagnostics(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		resellerResponse(c, nil, model.ErrResellerInvalid)
		return
	}
	data, err := model.ReadResellerDiagnostics(c.GetInt("id"), id)
	if err != nil {
		resellerResponse(c, nil, err)
		return
	}
	resellerResponse(c, gin.H{"ledger": data, "publisher": service.GetResellerPublisherHealth()}, nil)
}
func RecoverResellerUnsentRequest(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		resellerResponse(c, nil, model.ErrResellerInvalid)
		return
	}
	if err := model.ResellerManageTarget(model.DB, c.GetInt("id"), id, true); err != nil {
		resellerResponse(c, nil, err)
		return
	}
	var request model.ResellerRequest
	if err := model.DB.First(&request, "id = ? AND agent_user_id = ?", c.Param("request_id"), id).Error; err != nil {
		resellerResponse(c, nil, err)
		return
	}
	resellerResponse(c, nil, service.RecoverUnsentPersonalRequest(c.Request.Context(), request.ID))
}
func RebuildResellerProjection(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		resellerResponse(c, nil, model.ErrResellerInvalid)
		return
	}
	resellerResponse(c, nil, model.RebuildResellerAccountProjection(c.GetInt("id"), id))
}
