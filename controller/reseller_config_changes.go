package controller

import (
	"strconv"

	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

func GetResellerConfigChanges(c *gin.Context) {
	userID, idErr := strconv.Atoi(c.Param("id"))
	before, cursorErr := strconv.ParseInt(c.DefaultQuery("before", "0"), 10, 64)
	if idErr != nil || cursorErr != nil {
		resellerResponse(c, nil, model.ErrResellerInvalid)
		return
	}
	page, err := model.ReadResellerConfigChanges(c.GetInt("id"), userID, before)
	resellerResponse(c, page, err)
}
