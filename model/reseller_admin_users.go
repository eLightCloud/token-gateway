package model

import (
	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"strconv"
)

type ResellerAdminUserFilter struct {
	Kind    string
	AgentID int
	Keyword string
	Group   string
	Role    string
	Status  string
}

// The existing list stays authoritative without reseller filters. This query
// adds only the identity/parent slice and preserves its paging and sort contract.
func QueryResellerAdministrativeUsers(filter ResellerAdminUserFilter, page *common.PageInfo, order UserSortOptions) ([]*User, int64, error) {
	if filter.Kind != "" && filter.Kind != "ordinary" && filter.Kind != "agent" && filter.Kind != "agent_customer" || filter.AgentID < 0 {
		return nil, 0, ErrResellerInvalid
	}
	var users []*User
	var total int64
	err := DB.Transaction(func(tx *gorm.DB) error {
		query := tx.Unscoped().Model(&User{})
		if filter.Kind != "" {
			kind := filter.Kind
			if kind == "ordinary" {
				kind = ""
			}
			query = query.Where("reseller_kind = ?", kind)
		}
		if filter.AgentID > 0 {
			query = query.Where("id IN (?)", tx.Model(&ResellerIdentity{}).Select("user_id").Where("agent_user_id = ? AND kind = ?", filter.AgentID, "agent_customer"))
		}
		if filter.Keyword != "" {
			pattern := "%" + filter.Keyword + "%"
			if id, err := strconv.Atoi(filter.Keyword); err == nil {
				query = query.Where("id = ? OR username LIKE ? OR email LIKE ? OR display_name LIKE ?", id, pattern, pattern, pattern)
			} else {
				query = query.Where("username LIKE ? OR email LIKE ? OR display_name LIKE ?", pattern, pattern, pattern)
			}
		}
		if filter.Group != "" {
			query = query.Where(commonGroupCol+" = ?", filter.Group)
		}
		for _, item := range []struct{ Column, Value string }{{"role", filter.Role}, {"status", filter.Status}} {
			if item.Value != "" {
				number, err := strconv.Atoi(item.Value)
				if err != nil {
					return ErrResellerInvalid
				}
				query = query.Where(item.Column+" = ?", number)
			}
		}
		if err := query.Count(&total).Error; err != nil {
			return err
		}
		return order.Apply(query).Omit("password", "access_token").Offset(page.GetStartIdx()).Limit(page.GetPageSize()).Find(&users).Error
	})
	return users, total, err
}
