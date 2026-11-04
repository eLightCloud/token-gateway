package controller

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"strconv"
)

// Identity metadata is added only to administrative list DTOs.
type resellerAdminUserView struct {
	*model.User
	ResellerKind string `json:"reseller_kind"`
	AgentUserID  int    `json:"agent_user_id,omitempty"`
}

func writeResellerAdminUserPage(c *gin.Context, page *common.PageInfo, users []*model.User, total int64) {
	views := make([]resellerAdminUserView, len(users))
	for i, user := range users {
		views[i] = resellerAdminUserView{User: user, ResellerKind: user.ResellerKind}
		if user.ResellerKind == "agent_customer" {
			identity, err := model.GetResellerIdentity(model.DB, user.Id)
			if err != nil {
				resellerResponse(c, nil, err)
				return
			}
			if identity == nil {
				resellerResponse(c, nil, model.ErrResellerConflict)
				return
			}
			views[i].AgentUserID = identity.AgentUserID
		}
	}
	page.SetTotal(int(total))
	page.SetItems(views)
	common.ApiSuccess(c, page)
}

func serveResellerAdministrativeUsers(c *gin.Context) bool {
	if c.Query("reseller_kind") == "" && c.Query("agent_id") == "" {
		return false
	}
	agentID := 0
	if value := c.Query("agent_id"); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			resellerResponse(c, nil, model.ErrResellerInvalid)
			return true
		}
		agentID = parsed
	}
	page := common.GetPageQuery(c)
	users, total, err := model.QueryResellerAdministrativeUsers(model.ResellerAdminUserFilter{Kind: c.Query("reseller_kind"), AgentID: agentID, Keyword: c.Query("keyword"), Group: c.Query("group"), Role: c.Query("role"), Status: c.Query("status")}, page, model.NewUserSortOptions(c.Query("sort_by"), c.Query("sort_order")))
	if err != nil {
		resellerResponse(c, nil, err)
		return true
	}
	writeResellerAdminUserPage(c, page, users, total)
	return true
}
