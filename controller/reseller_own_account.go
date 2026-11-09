package controller

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

func GetResellerOwnAccount(c *gin.Context) {
	identity, ok := middleware.GetSessionAuthIdentity(c)
	if !ok {
		resellerResponse(c, nil, model.ErrResellerForbidden)
		return
	}
	account, err := model.ReadResellerOwnAccount(identity.UserID)
	resellerResponse(c, account, err)
}

func UpdateResellerOwnProfile(c *gin.Context) {
	identity, ok := middleware.GetSessionAuthIdentity(c)
	if !ok {
		resellerResponse(c, nil, model.ErrResellerForbidden)
		return
	}
	var input model.ResellerOwnProfileInput
	if err := common.DecodeJson(c.Request.Body, &input); err != nil {
		resellerResponse(c, nil, model.ErrResellerInvalid)
		return
	}
	err := model.UpdateResellerOwnProfile(identity.UserID, input)
	if err == nil {
		recordUserSecurityAudit(c, identity.UserID, "reseller.profile.update", nil)
	}
	resellerResponse(c, nil, err)
}
