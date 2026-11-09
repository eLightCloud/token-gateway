package model

import (
	"fmt"
	"slices"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/types"
	"gorm.io/gorm"
)

// Dedicated white-list DTOs never embed User, Task, Log, or credential structs.
type ResellerUserView struct {
	UserID           int              `json:"user_id"`
	Username         string           `json:"username"`
	DisplayName      string           `json:"display_name"`
	Email            string           `json:"email"`
	Phone            string           `json:"phone,omitempty"`
	Remark           string           `json:"remark,omitempty"`
	Activation       string           `json:"activation"`
	AgentDisabled    bool             `json:"agent_disabled"`
	PlatformDisabled bool             `json:"platform_disabled"`
	Quota            int64            `json:"quota"`
	Account          *ResellerAccount `json:"account,omitempty"`
}
type ResellerUserPage struct {
	Items      []ResellerUserView `json:"items"`
	NextCursor int                `json:"next_cursor"`
	Account    *ResellerAccount   `json:"account,omitempty"`
}

func ListResellerUsers(operatorID int, admin bool, after int, search string, activation string, serviceStatus string, limit int) (*ResellerUserPage, error) {
	if after < 0 || limit < 1 || limit > 100 || len(search) > 100 {
		return nil, ErrResellerInvalid
	}
	var operator User
	if err := DB.First(&operator, operatorID).Error; err != nil {
		return nil, err
	}
	if operator.Status != common.UserStatusEnabled {
		return nil, ErrResellerForbidden
	}
	query := DB.Model(&ResellerIdentity{}).Where("user_id > ?", after)
	result := &ResellerUserPage{Items: []ResellerUserView{}}
	if admin {
		if operator.Role < common.RoleAdminUser || operator.ResellerKind != "" {
			return nil, ErrResellerForbidden
		}
		query = query.Where("kind = ?", "agent")
	} else {
		var account ResellerAccount
		if err := DB.First(&account, "user_id = ?", operatorID).Error; err != nil {
			return nil, err
		}
		if operator.ResellerKind != "agent" || !account.Enabled {
			return nil, ErrResellerForbidden
		}
		result.Account = &account
		query = query.Where("kind = ? AND agent_user_id = ?", "agent_customer", operatorID)
	}
	switch activation {
	case "":
	case "pending", "security_pending", "active":
		query = query.Where("activation = ?", activation)
	case "password_set":
		if !admin {
			return nil, ErrResellerInvalid
		}
		query = query.Where("activation IN ?", []string{"security_pending", "active"})
	default:
		return nil, ErrResellerInvalid
	}
	switch serviceStatus {
	case "":
	case "disabled":
		if !admin {
			return nil, ErrResellerInvalid
		}
		platformDisabled := DB.Model(&User{}).Select("id").Where("status <> ?", common.UserStatusEnabled)
		accountDisabled := DB.Model(&ResellerAccount{}).Select("user_id").Where("enabled = ?", false)
		query = query.Where(DB.Where("user_id IN (?)", platformDisabled).Or("user_id IN (?)", accountDisabled))
	case "platform_disabled":
		query = query.Where("user_id IN (?)", DB.Model(&User{}).Select("id").Where("status <> ?", common.UserStatusEnabled))
	case "agent_disabled":
		if admin {
			query = query.Where("user_id IN (?)", DB.Model(&ResellerAccount{}).Select("user_id").Where("enabled = ?", false))
		} else {
			query = query.Where("agent_disabled = ?", true)
		}
	case "enabled":
		query = query.Where("user_id IN (?)", DB.Model(&User{}).Select("id").Where("status = ?", common.UserStatusEnabled))
		if admin {
			query = query.Where("user_id IN (?)", DB.Model(&ResellerAccount{}).Select("user_id").Where("enabled = ?", true))
		} else {
			query = query.Where("agent_disabled = ?", false)
		}
	default:
		return nil, ErrResellerInvalid
	}
	if search != "" {
		query = query.Where("user_id IN (?)", DB.Model(&User{}).Select("id").Where("username LIKE ? OR email LIKE ?", "%"+search+"%", "%"+search+"%"))
	}
	var identities []ResellerIdentity
	if err := query.Order("user_id").Limit(limit + 1).Find(&identities).Error; err != nil {
		return nil, err
	}
	if len(identities) > limit {
		identities = identities[:limit]
		result.NextCursor = identities[len(identities)-1].UserID
	}
	for _, identity := range identities {
		var user User
		if err := DB.Select("id", "username", "display_name", "email", "quota", "status").First(&user, identity.UserID).Error; err != nil {
			return nil, err
		}
		view := ResellerUserView{UserID: user.Id, Username: user.Username, DisplayName: user.DisplayName, Email: user.Email, Quota: user.Quota, Activation: identity.Activation, AgentDisabled: identity.AgentDisabled, PlatformDisabled: user.Status != common.UserStatusEnabled}
		if admin {
			view.Phone = identity.Phone
			var account ResellerAccount
			if err := DB.First(&account, "user_id = ?", user.Id).Error; err != nil {
				return nil, err
			}
			view.Account = &account
		}
		result.Items = append(result.Items, view)
	}
	return result, nil
}

func SetResellerEnabled(operatorID, userID int, admin, enabled bool) error {
	return DB.Transaction(func(tx *gorm.DB) error {
		if err := ResellerManageTarget(tx, operatorID, userID, admin); err != nil {
			return err
		}
		if admin {
			var identity ResellerIdentity
			if err := tx.First(&identity, "user_id = ? AND kind = ?", userID, "agent").Error; err != nil {
				return err
			}
			return tx.Model(&ResellerAccount{}).Where("user_id = ?", userID).Update("enabled", enabled).Error
		}
		return tx.Model(&ResellerIdentity{}).Where("user_id = ?", userID).Update("agent_disabled", !enabled).Error
	})
}

type ResellerBillingFilter struct {
	RequestID           string
	IncludeReservations bool
	ProjectionRevision  *int64
	CustomerID          int
	Model               string
	Kind                string
	Posting             string
	After               int64
	From                int64
	Until               int64
	Limit               int
}
type ResellerBillingRow struct {
	Usage               map[string]float64 `json:"usage"`
	CustomerRatio       string             `json:"customer_ratio"`
	AgentRatio          string             `json:"agent_ratio"`
	ID                  int64              `json:"id"`
	RequestID           string             `json:"request_id"`
	CustomerID          int                `json:"customer_id"`
	Model               string             `json:"model"`
	ChannelID           int                `json:"channel_id"`
	Kind                string             `json:"kind"`
	BaseQuota           int64              `json:"base_quota"`
	CustomerDelta       int64              `json:"customer_delta"`
	CustomerTarget      *int64             `json:"customer_target"`
	AgentTarget         *int64             `json:"agent_target"`
	Margin              *int64             `json:"margin"`
	ReserveDelta        int64              `json:"reserve_delta"`
	SpendDelta          int64              `json:"spend_delta"`
	OccurredAt          int64              `json:"occurred_at"`
	ProcessedAt         int64              `json:"processed_at"`
	OriginalOperationID string             `json:"original_operation_id"`
}
type ResellerBillingPage struct {
	ProjectionRevision int64                `json:"projection_revision"`
	Items              []ResellerBillingRow `json:"items"`
	NextCursor         int64                `json:"next_cursor"`
	Until              int64                `json:"until"`
	Account            ResellerAccount      `json:"account"`
}

func QueryResellerBilling(tx *gorm.DB, operatorID int, filter ResellerBillingFilter) (*ResellerBillingPage, error) {
	if len(filter.RequestID) > 64 || filter.Limit < 1 || filter.Limit > 500 || filter.After < 0 || filter.From < 0 || filter.Until <= filter.From || filter.Until > common.GetTimestamp()+1 || filter.CustomerID < 0 || len(filter.Model) > 512 || filter.Kind != "" && filter.Kind != "reserve" && filter.Kind != "settle" && filter.Kind != "refund" {
		return nil, ErrResellerInvalid
	}
	var identity ResellerIdentity
	if err := tx.First(&identity, "user_id = ? AND kind = ?", operatorID, "agent").Error; err != nil {
		return nil, err
	}
	if identity.Activation != "active" {
		return nil, ErrResellerForbidden
	}
	result := &ResellerBillingPage{Items: []ResellerBillingRow{}, Until: filter.Until}
	if err := tx.First(&result.Account, "user_id = ?", operatorID).Error; err != nil {
		return nil, err
	}
	result.ProjectionRevision = result.Account.Revision
	if filter.ProjectionRevision != nil {
		if *filter.ProjectionRevision < 0 || *filter.ProjectionRevision > result.Account.Revision {
			return nil, ErrResellerConflict
		}
		result.ProjectionRevision = *filter.ProjectionRevision
	}
	if !result.Account.Enabled {
		return nil, ErrResellerForbidden
	}
	if filter.CustomerID > 0 {
		if err := ResellerManageTarget(tx, operatorID, filter.CustomerID, false); err != nil {
			return nil, err
		}
	}
	query := tx.Where("agent_user_id = ? AND id > ? AND occurred_at >= ? AND occurred_at < ?", operatorID, filter.After, filter.From, filter.Until)
	latest := tx.Model(&ResellerEvent{}).Select("MAX(id)").Where("agent_user_id = ? AND occurred_at < ?", operatorID, filter.Until).Group("request_id")
	if !filter.IncludeReservations {
		query = query.Where("kind <> ? OR id IN (?)", "reserve", latest)
	}
	switch filter.Posting {
	case "":
	case "pending":
		query = query.Where("processed_revision = ? OR processed_revision > ?", 0, result.ProjectionRevision)
	case "posted":
		query = query.Where("processed_revision > ? AND processed_revision <= ?", 0, result.ProjectionRevision)
	default:
		return nil, ErrResellerInvalid
	}
	if filter.CustomerID > 0 {
		query = query.Where("customer_user_id = ?", filter.CustomerID)
	}
	if filter.RequestID != "" {
		query = query.Where("request_id = ?", filter.RequestID)
	}
	if filter.Model != "" {
		query = query.Where("model = ?", filter.Model)
	}
	if filter.Kind != "" {
		query = query.Where("kind = ?", filter.Kind)
	}
	var events []ResellerEvent
	if err := query.Order("id").Limit(filter.Limit + 1).Find(&events).Error; err != nil {
		return nil, err
	}
	if len(events) > filter.Limit {
		events = events[:filter.Limit]
		result.NextCursor = events[len(events)-1].ID
	}
	for _, event := range events {
		var fact types.PersonalBillingFact
		if err := common.UnmarshalJsonStr(event.Snapshot, &fact); err != nil {
			return nil, err
		}
		if event.ProcessedRevision == 0 || event.ProcessedRevision > result.ProjectionRevision {
			event.ProcessedAt = 0
		}
		row := ResellerBillingRow{Usage: fact.Usage, CustomerRatio: formatPersonalRatio(fact.CustomerRatio), AgentRatio: formatPersonalRatio(fact.AgentRatio), CustomerDelta: event.CustomerDelta, ID: event.ID, RequestID: event.RequestID, CustomerID: event.CustomerUserID, Model: event.Model, ChannelID: event.ChannelID, Kind: event.Kind, BaseQuota: event.BaseQuota, ReserveDelta: event.ReserveDelta, SpendDelta: event.SpendDelta, OccurredAt: event.OccurredAt, ProcessedAt: event.ProcessedAt, OriginalOperationID: event.OriginalOperationID}
		if event.Kind != "reserve" {
			customer, agent, margin := event.CustomerTarget, event.AgentTarget, event.CustomerDelta-event.SpendDelta
			row.CustomerTarget = &customer
			row.AgentTarget = &agent
			row.Margin = &margin
		}
		result.Items = append(result.Items, row)
	}
	return result, nil
}

func ReadResellerAdjustments(operatorID, userID int, admin bool, after int64, limit int) ([]ResellerQuotaAdjustment, error) {
	if limit < 1 || limit > 500 || after < 0 {
		return nil, ErrResellerInvalid
	}
	if err := ResellerManageTarget(DB, operatorID, userID, admin); err != nil {
		return nil, err
	}
	var rows []ResellerQuotaAdjustment
	err := DB.Where("user_id = ? AND id > ?", userID, after).Order("id").Limit(limit).Find(&rows).Error
	return rows, err
}

type PersonalRatioRange struct {
	Model string `json:"model"`
	Min   string `json:"min"`
	Max   string `json:"max"`
}

func PersonalRatioRanges(userID int, groups map[string]string) ([]PersonalRatioRange, error) {
	var abilities []Ability
	if err := DB.Where("enabled = ?", true).Find(&abilities).Error; err != nil {
		return nil, err
	}
	byModel := map[string][]Ability{}
	for _, ability := range abilities {
		if _, usable := groups[ability.Group]; usable {
			byModel[ability.Model] = append(byModel[ability.Model], ability)
		}
	}
	modelNames := make([]string, 0, len(byModel))
	for name := range byModel {
		modelNames = append(modelNames, name)
	}
	slices.Sort(modelNames)
	snapshots, err := LoadPersonalDisplayPricings(userID, modelNames)
	if err != nil {
		return nil, err
	}
	ranges := make([]PersonalRatioRange, 0, len(modelNames))
	for _, name := range modelNames {
		snapshot := snapshots[name]
		var lo, hi int64
		for _, ability := range byModel[name] {
			ratio := ResellerRatioScale
			if snapshot != nil {
				price, ok := snapshot.Channels[ability.ChannelId]
				if !ok && snapshot.AgentUserID > 0 {
					continue
				}
				if ok {
					ratio = price.CustomerRatio
				}
			}
			if lo == 0 || ratio < lo {
				lo = ratio
			}
			hi = max(hi, ratio)
		}
		if lo > 0 {
			ranges = append(ranges, PersonalRatioRange{Model: name, Min: formatPersonalRatio(lo), Max: formatPersonalRatio(hi)})
		}
	}
	return ranges, nil
}

func formatPersonalRatio(ratio int64) string {
	value := strings.TrimRight(strings.TrimRight(fmt.Sprintf("%d.%06d", ratio/ResellerRatioScale, ratio%ResellerRatioScale), "0"), ".")
	return value
}
