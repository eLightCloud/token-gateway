package model

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/types"

	"gorm.io/gorm"
)

const (
	OrganizationStatusEnabled  = 1
	OrganizationStatusDisabled = 2

	OrganizationRoleAdmin  = "admin"
	OrganizationRoleMember = "member"
)

type Organization struct {
	Id        int    `json:"id"`
	Name      string `json:"name" gorm:"type:varchar(128);not null"`
	Status    int    `json:"status" gorm:"type:int;default:1;index"`
	CreatedAt int64  `json:"created_at" gorm:"autoCreateTime;column:created_at"`
	UpdatedAt int64  `json:"updated_at" gorm:"autoUpdateTime;column:updated_at"`
}

type OrganizationMember struct {
	Id             int     `json:"id"`
	OrganizationId int     `json:"organization_id" gorm:"index"`
	UserId         int     `json:"user_id" gorm:"index"`
	Role           string  `json:"role" gorm:"type:varchar(32);default:'member';index"`
	JoinedAt       int64   `json:"joined_at" gorm:"bigint;index"`
	LeftAt         int64   `json:"left_at" gorm:"bigint;default:0;index"`
	BillingStartAt int64   `json:"billing_start_at" gorm:"bigint;default:0"`
	CurrentKey     *string `json:"-" gorm:"type:varchar(64);uniqueIndex"`
	Username       string  `json:"username,omitempty" gorm:"-:all"`
	DisplayName    string  `json:"display_name,omitempty" gorm:"-:all"`
	Email          string  `json:"email,omitempty" gorm:"-:all"`
}

type OrganizationWithMember struct {
	Organization Organization       `json:"organization"`
	Member       OrganizationMember `json:"member"`
}

type OrganizationBillingFilters struct {
	StartTimestamp int64
	EndTimestamp   int64
	Types          []int
	UserId         int
	ModelName      string
	ChannelId      int
}

type OrganizationBillingSummary struct {
	TotalQuota        int `json:"total_quota"`
	RequestCount      int `json:"request_count"`
	PromptTokens      int `json:"prompt_tokens"`
	CompletionTokens  int `json:"completion_tokens"`
	MemberCount       int `json:"member_count"`
	ActiveMemberCount int `json:"active_member_count"`
}

type OrganizationBillingDimension struct {
	UserId           int              `json:"user_id,omitempty"`
	Username         string           `json:"username,omitempty"`
	DisplayName      string           `json:"display_name,omitempty"`
	ModelName        string           `json:"model_name,omitempty"`
	ChannelId        int              `json:"channel_id,omitempty"`
	ChannelName      string           `json:"channel_name,omitempty"`
	TotalQuota       int              `json:"total_quota"`
	RequestCount     int              `json:"request_count"`
	PromptTokens     int              `json:"prompt_tokens"`
	CompletionTokens int              `json:"completion_tokens"`
	Pricing          *PricingSnapshot `json:"pricing,omitempty" gorm:"-"`
}

type PricingSnapshot struct {
	QuotaType   int     `json:"quota_type"`
	ModelRatio  float64 `json:"model_ratio"`
	ModelPrice  float64 `json:"model_price"`
	BillingMode string  `json:"billing_mode,omitempty"`
	BillingExpr string  `json:"billing_expr,omitempty"`
	OwnerBy     string  `json:"owner_by,omitempty"`
}

type OrganizationBillingTrendPoint struct {
	Period           string `json:"period"`
	TotalQuota       int    `json:"total_quota"`
	RequestCount     int    `json:"request_count"`
	PromptTokens     int    `json:"prompt_tokens"`
	CompletionTokens int    `json:"completion_tokens"`
}

func MaskOrganizationBillingName(value string) string {
	name := []rune(strings.TrimSpace(value))
	switch len(name) {
	case 0:
		return ""
	case 1:
		return "*"
	case 2:
		return string(name[0]) + "*"
	default:
		return string(name[0]) + strings.Repeat("*", len(name)-2) + string(name[len(name)-1])
	}
}

func OrganizationBillingUsername(username string, userId int) string {
	if strings.TrimSpace(username) == "" && userId > 0 {
		return fmt.Sprintf("用户 #%d", userId)
	}
	return username
}

func normalizeOrganizationRole(role string) (string, error) {
	role = strings.ToLower(strings.TrimSpace(role))
	if role == "" {
		role = OrganizationRoleMember
	}
	switch role {
	case OrganizationRoleAdmin, OrganizationRoleMember:
		return role, nil
	default:
		return "", fmt.Errorf("invalid organization role: %s", role)
	}
}

func migrateOrganizationRoles() error {
	return DB.Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&OrganizationMember{}).Where("role = ?", "owner").Update("role", OrganizationRoleAdmin).Error; err != nil {
			return err
		}
		return tx.Model(&OrganizationMember{}).Where("role = ?", "billing").Update("role", OrganizationRoleMember).Error
	})
}

func activeOrganizationCurrentKey(userId int) *string {
	key := strconv.Itoa(userId)
	return &key
}

func CreateOrganization(name string) (*Organization, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("organization name is required")
	}

	now := common.GetTimestamp()
	org := Organization{
		Name:      name,
		Status:    OrganizationStatusEnabled,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := DB.Create(&org).Error; err != nil {
		return nil, err
	}
	return &org, nil
}

func ensureUserHasNoActiveOrganization(tx *gorm.DB, userId int) error {
	var count int64
	if err := tx.Model(&OrganizationMember{}).Where("user_id = ? AND left_at = 0", userId).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return errors.New("user already belongs to an organization")
	}
	return nil
}

func lockOrganizationForMembershipChange(tx *gorm.DB, organizationId int) error {
	var organization Organization
	return lockForUpdate(tx).Select("id").First(&organization, "id = ?", organizationId).Error
}

func ensureOrganizationHasAnotherAdmin(tx *gorm.DB, organizationId int, excludedMemberId int) error {
	var count int64
	if err := tx.Model(&OrganizationMember{}).
		Where("organization_id = ? AND role = ? AND left_at = 0 AND id <> ?", organizationId, OrganizationRoleAdmin, excludedMemberId).
		Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return errors.New("cannot remove the last organization admin")
	}
	return nil
}

func GetOrganizationById(id int) (*Organization, error) {
	if id <= 0 {
		return nil, errors.New("invalid organization id")
	}
	var org Organization
	if err := DB.First(&org, "id = ?", id).Error; err != nil {
		return nil, err
	}
	return &org, nil
}

func GetCurrentOrganizationForUser(userId int) (*OrganizationWithMember, error) {
	var members []OrganizationMember
	if err := DB.
		Where("user_id = ? AND left_at = 0", userId).
		Order("id asc").
		Limit(2).
		Find(&members).Error; err != nil {
		return nil, err
	}
	if len(members) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	if len(members) > 1 {
		return nil, errors.New("user has multiple active organization memberships")
	}
	member := members[0]
	var org Organization
	if err := DB.First(&org, "id = ?", member.OrganizationId).Error; err != nil {
		return nil, err
	}
	return &OrganizationWithMember{Organization: org, Member: member}, nil
}

func ListOrganizations(keyword string, status *int, startIdx int, num int) ([]Organization, int64, error) {
	keyword = strings.TrimSpace(keyword)
	tx := DB.Model(&Organization{})
	if keyword != "" {
		like := "%" + keyword + "%"
		keywordClauses := []string{
			"organizations.name LIKE ?",
		}
		keywordArgs := []interface{}{like}
		if keywordId, err := strconv.Atoi(keyword); err == nil {
			keywordClauses = append(keywordClauses, "organizations.id = ?")
			keywordArgs = append(keywordArgs, keywordId)
		}
		tx = tx.Where("("+strings.Join(keywordClauses, " OR ")+")", keywordArgs...)
	}
	if status != nil {
		switch *status {
		case OrganizationStatusEnabled, OrganizationStatusDisabled:
			tx = tx.Where("organizations.status = ?", *status)
		default:
			return nil, 0, errors.New("invalid organization status")
		}
	}
	var total int64
	if err := tx.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var orgs []Organization
	err := tx.Order("organizations.id desc").Limit(num).Offset(startIdx).Find(&orgs).Error
	return orgs, total, err
}

func UpdateOrganization(id int, name string, status *int) (*Organization, error) {
	name = strings.TrimSpace(name)
	updates := map[string]interface{}{}
	if name != "" {
		updates["name"] = name
	}
	if status != nil {
		switch *status {
		case OrganizationStatusEnabled, OrganizationStatusDisabled:
			updates["status"] = *status
		default:
			return nil, errors.New("invalid organization status")
		}
	}
	if len(updates) == 0 {
		return GetOrganizationById(id)
	}
	if err := DB.Model(&Organization{}).Where("id = ?", id).Updates(updates).Error; err != nil {
		return nil, err
	}
	return GetOrganizationById(id)
}

func ListOrganizationMembers(organizationId int, includeHistory bool) ([]OrganizationMember, error) {
	tx := DB.Where("organization_id = ?", organizationId)
	if !includeHistory {
		tx = tx.Where("left_at = 0")
	}
	var members []OrganizationMember
	if err := tx.Order("left_at asc, role desc, joined_at asc, id asc").Find(&members).Error; err != nil {
		return nil, err
	}
	fillOrganizationMemberUsers(members)
	return members, nil
}

func fillOrganizationMemberUsers(members []OrganizationMember) {
	if len(members) == 0 {
		return
	}
	userIds := make([]int, 0, len(members))
	for _, member := range members {
		userIds = append(userIds, member.UserId)
	}
	var users []User
	if err := DB.Select("id", "username", "display_name", "email").Where("id IN ?", userIds).Find(&users).Error; err != nil {
		return
	}
	userMap := make(map[int]User, len(users))
	for _, user := range users {
		userMap[user.Id] = user
	}
	for i := range members {
		user, ok := userMap[members[i].UserId]
		if !ok {
			continue
		}
		members[i].Username = user.Username
		members[i].DisplayName = user.DisplayName
		members[i].Email = user.Email
	}
}

func AddOrganizationMember(organizationId int, userId int, role string) (*OrganizationMember, error) {
	if organizationId <= 0 || userId <= 0 {
		return nil, errors.New("invalid organization or user id")
	}
	normalizedRole, err := normalizeOrganizationRole(role)
	if err != nil {
		return nil, err
	}
	user, err := GetUserById(userId, false)
	if err != nil {
		return nil, err
	}
	if user.Status != common.UserStatusEnabled {
		return nil, errors.New("user is disabled")
	}
	if user.Role >= common.RoleAdminUser {
		return nil, errors.New("system administrators cannot be added to organizations")
	}
	now := common.GetTimestamp()
	member := OrganizationMember{
		OrganizationId: organizationId,
		UserId:         userId,
		Role:           normalizedRole,
		JoinedAt:       now,
		BillingStartAt: now,
		CurrentKey:     activeOrganizationCurrentKey(userId),
	}
	err = DB.Transaction(func(tx *gorm.DB) error {
		if err := lockOrganizationForMembershipChange(tx, organizationId); err != nil {
			return err
		}
		if err := ensureUserHasNoActiveOrganization(tx, userId); err != nil {
			return err
		}
		if err := tx.Create(&member).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	fillOrganizationMemberUsersInPlace(&member)
	return &member, nil
}

func UpdateOrganizationMemberRole(organizationId int, userId int, role string) (*OrganizationMember, error) {
	normalizedRole, err := normalizeOrganizationRole(role)
	if err != nil {
		return nil, err
	}
	var member OrganizationMember
	err = DB.Transaction(func(tx *gorm.DB) error {
		if err := lockOrganizationForMembershipChange(tx, organizationId); err != nil {
			return err
		}
		if err := tx.Where("organization_id = ? AND user_id = ? AND left_at = 0", organizationId, userId).First(&member).Error; err != nil {
			return err
		}
		if member.Role == OrganizationRoleAdmin && normalizedRole != OrganizationRoleAdmin {
			if err := ensureOrganizationHasAnotherAdmin(tx, organizationId, member.Id); err != nil {
				return err
			}
		}
		return tx.Model(&OrganizationMember{}).Where("id = ?", member.Id).Update("role", normalizedRole).Error
	})
	if err != nil {
		return nil, err
	}
	member.Role = normalizedRole
	fillOrganizationMemberUsersInPlace(&member)
	return &member, nil
}

func RemoveOrganizationMember(organizationId int, userId int) error {
	now := common.GetTimestamp()
	return DB.Transaction(func(tx *gorm.DB) error {
		if err := lockOrganizationForMembershipChange(tx, organizationId); err != nil {
			return err
		}
		var member OrganizationMember
		if err := tx.Where("organization_id = ? AND user_id = ? AND left_at = 0", organizationId, userId).First(&member).Error; err != nil {
			return err
		}
		if member.Role == OrganizationRoleAdmin {
			if err := ensureOrganizationHasAnotherAdmin(tx, organizationId, member.Id); err != nil {
				return err
			}
		}
		return tx.Model(&OrganizationMember{}).Where("id = ?", member.Id).Updates(map[string]interface{}{
			"left_at":     now,
			"current_key": nil,
		}).Error
	})
}

func UserCanManageOrganization(userId int, organizationId int) (bool, error) {
	return userHasOrganizationRoles(userId, organizationId, OrganizationRoleAdmin)
}

func UserCanViewOrganizationBilling(userId int, organizationId int) (bool, error) {
	return userHasOrganizationRoles(userId, organizationId, OrganizationRoleAdmin)
}

func userHasOrganizationRoles(userId int, organizationId int, roles ...string) (bool, error) {
	var member OrganizationMember
	err := DB.Where("organization_id = ? AND user_id = ? AND left_at = 0", organizationId, userId).First(&member).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return false, nil
		}
		return false, err
	}
	for _, role := range roles {
		if member.Role == role {
			return true, nil
		}
	}
	return false, nil
}

func activeAndHistoricalOrganizationMembers(organizationId int, userId int) ([]OrganizationMember, error) {
	tx := DB.Where("organization_id = ?", organizationId)
	if userId > 0 {
		tx = tx.Where("user_id = ?", userId)
	}
	var members []OrganizationMember
	if err := tx.Order("joined_at asc, id asc").Find(&members).Error; err != nil {
		return nil, err
	}
	return members, nil
}

// effectiveBillingStart 返回该段成员关系的组织账单归属起点：BillingStartAt > 0 时取
// BillingStartAt，否则回退 JoinedAt。回退分支仅服务功能上线前的存量记录（零值过渡态）；
// 新代码路径永不写入 0：新增成员写 JoinedAt，回填写经校验的大于零的候选值。
func effectiveBillingStart(member OrganizationMember) int64 {
	if member.BillingStartAt > 0 {
		return member.BillingStartAt
	}
	return member.JoinedAt
}

func logMembershipBounds(member OrganizationMember, filters OrganizationBillingFilters) (int64, int64, bool, bool) {
	start := effectiveBillingStart(member)
	if filters.StartTimestamp > start {
		start = filters.StartTimestamp
	}
	if member.LeftAt > 0 && filters.StartTimestamp >= member.LeftAt {
		return 0, 0, false, false
	}
	if filters.EndTimestamp > 0 && filters.EndTimestamp < start {
		return 0, 0, false, false
	}

	end := filters.EndTimestamp
	exclusiveEnd := false
	if member.LeftAt > 0 && (end == 0 || member.LeftAt <= end) {
		end = member.LeftAt
		exclusiveEnd = true
	}
	return start, end, exclusiveEnd, true
}

func applyOrganizationLogFilters(tx *gorm.DB, member OrganizationMember, filters OrganizationBillingFilters) (*gorm.DB, bool, error) {
	start, end, exclusiveEnd, ok := logMembershipBounds(member, filters)
	if !ok {
		return tx, false, nil
	}
	tx = tx.Where("user_id = ?", member.UserId).Where("created_at >= ?", start)
	if end > 0 {
		if exclusiveEnd {
			tx = tx.Where("created_at < ?", end)
		} else {
			tx = tx.Where("created_at <= ?", end)
		}
	}
	typesFilter := filters.Types
	if len(typesFilter) == 0 {
		typesFilter = []int{LogTypeConsume}
	}
	if len(typesFilter) == 1 {
		tx = tx.Where("type = ?", typesFilter[0])
	} else {
		tx = tx.Where("type IN ?", typesFilter)
	}
	if filters.ModelName != "" {
		var err error
		tx, err = applyExplicitLogTextFilter(tx, "model_name", filters.ModelName)
		if err != nil {
			return tx, false, err
		}
	}
	if filters.ChannelId > 0 {
		tx = tx.Where("channel_id = ?", filters.ChannelId)
	}
	return tx, true, nil
}

type organizationLogAggregate struct {
	TotalQuota       int
	RequestCount     int
	PromptTokens     int
	CompletionTokens int
}

func aggregateOrganizationLogs(members []OrganizationMember, filters OrganizationBillingFilters, each func(OrganizationMember, organizationLogAggregate)) error {
	for _, member := range members {
		tx, ok, err := applyOrganizationLogFilters(LOG_DB.Model(&Log{}), member, filters)
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		var row organizationLogAggregate
		if err := tx.Select("COALESCE(sum(quota), 0) AS total_quota, count(*) AS request_count, COALESCE(sum(prompt_tokens), 0) AS prompt_tokens, COALESCE(sum(completion_tokens), 0) AS completion_tokens").Scan(&row).Error; err != nil {
			return err
		}
		each(member, row)
	}
	return nil
}

func GetOrganizationBillingSummary(organizationId int, filters OrganizationBillingFilters) (*OrganizationBillingSummary, error) {
	members, err := activeAndHistoricalOrganizationMembers(organizationId, filters.UserId)
	if err != nil {
		return nil, err
	}
	summary := &OrganizationBillingSummary{}
	activeUsers := types.NewSet[int]()
	allUsers := types.NewSet[int]()
	for _, member := range members {
		allUsers.Add(member.UserId)
		if member.LeftAt == 0 {
			activeUsers.Add(member.UserId)
		}
	}
	summary.MemberCount = allUsers.Len()
	summary.ActiveMemberCount = activeUsers.Len()
	err = aggregateOrganizationLogs(members, filters, func(_ OrganizationMember, row organizationLogAggregate) {
		summary.TotalQuota += row.TotalQuota
		summary.RequestCount += row.RequestCount
		summary.PromptTokens += row.PromptTokens
		summary.CompletionTokens += row.CompletionTokens
	})
	return summary, err
}

func GetOrganizationBillingMembers(organizationId int, filters OrganizationBillingFilters) ([]OrganizationBillingDimension, error) {
	members, err := activeAndHistoricalOrganizationMembers(organizationId, filters.UserId)
	if err != nil {
		return nil, err
	}
	memberMap := make(map[int]*OrganizationBillingDimension)
	if err := aggregateOrganizationLogs(members, filters, func(member OrganizationMember, row organizationLogAggregate) {
		item, ok := memberMap[member.UserId]
		if !ok {
			item = &OrganizationBillingDimension{UserId: member.UserId}
			memberMap[member.UserId] = item
		}
		item.TotalQuota += row.TotalQuota
		item.RequestCount += row.RequestCount
		item.PromptTokens += row.PromptTokens
		item.CompletionTokens += row.CompletionTokens
	}); err != nil {
		return nil, err
	}
	items := make([]OrganizationBillingDimension, 0, len(memberMap))
	for _, item := range memberMap {
		items = append(items, *item)
	}
	fillBillingDimensionUsers(items)
	sortBillingDimensions(items)
	return items, nil
}

func fillBillingDimensionUsers(items []OrganizationBillingDimension) {
	if len(items) == 0 {
		return
	}
	userIds := make([]int, 0, len(items))
	for _, item := range items {
		if item.UserId > 0 {
			userIds = append(userIds, item.UserId)
		}
	}
	if len(userIds) == 0 {
		return
	}
	var users []User
	if err := DB.Select("id", "username", "display_name").Where("id IN ?", userIds).Find(&users).Error; err != nil {
		common.SysError(fmt.Sprintf("failed to hydrate organization billing users: %s", err.Error()))
		return
	}
	userMap := make(map[int]User, len(users))
	for _, user := range users {
		userMap[user.Id] = user
	}
	for i := range items {
		user, ok := userMap[items[i].UserId]
		if !ok {
			items[i].Username = OrganizationBillingUsername("", items[i].UserId)
			continue
		}
		items[i].Username = OrganizationBillingUsername(user.Username, items[i].UserId)
		items[i].DisplayName = MaskOrganizationBillingName(user.DisplayName)
	}
}

func GetOrganizationBillingModels(organizationId int, filters OrganizationBillingFilters) ([]OrganizationBillingDimension, error) {
	members, err := activeAndHistoricalOrganizationMembers(organizationId, filters.UserId)
	if err != nil {
		return nil, err
	}
	itemMap := make(map[string]*OrganizationBillingDimension)
	for _, member := range members {
		tx, ok, err := applyOrganizationLogFilters(LOG_DB.Model(&Log{}), member, filters)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		var rows []OrganizationBillingDimension
		if err := tx.Select("model_name, COALESCE(sum(quota), 0) AS total_quota, count(*) AS request_count, COALESCE(sum(prompt_tokens), 0) AS prompt_tokens, COALESCE(sum(completion_tokens), 0) AS completion_tokens").Group("model_name").Scan(&rows).Error; err != nil {
			return nil, err
		}
		for _, row := range rows {
			item, ok := itemMap[row.ModelName]
			if !ok {
				item = &OrganizationBillingDimension{ModelName: row.ModelName}
				itemMap[row.ModelName] = item
			}
			item.TotalQuota += row.TotalQuota
			item.RequestCount += row.RequestCount
			item.PromptTokens += row.PromptTokens
			item.CompletionTokens += row.CompletionTokens
		}
	}
	pricingMap := currentPricingSnapshotMap()
	items := make([]OrganizationBillingDimension, 0, len(itemMap))
	for _, item := range itemMap {
		if pricing, ok := pricingMap[item.ModelName]; ok {
			item.Pricing = &pricing
		}
		items = append(items, *item)
	}
	sortBillingDimensions(items)
	return items, nil
}

func currentPricingSnapshotMap() map[string]PricingSnapshot {
	pricing := GetPricing()
	result := make(map[string]PricingSnapshot, len(pricing))
	for _, item := range pricing {
		result[item.ModelName] = PricingSnapshot{
			QuotaType:   item.QuotaType,
			ModelRatio:  item.ModelRatio,
			ModelPrice:  item.ModelPrice,
			BillingMode: item.BillingMode,
			BillingExpr: item.BillingExpr,
			OwnerBy:     item.OwnerBy,
		}
	}
	return result
}

func GetOrganizationBillingChannels(organizationId int, filters OrganizationBillingFilters) ([]OrganizationBillingDimension, error) {
	members, err := activeAndHistoricalOrganizationMembers(organizationId, filters.UserId)
	if err != nil {
		return nil, err
	}
	itemMap := make(map[int]*OrganizationBillingDimension)
	for _, member := range members {
		tx, ok, err := applyOrganizationLogFilters(LOG_DB.Model(&Log{}), member, filters)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		var rows []OrganizationBillingDimension
		if err := tx.Select("channel_id, COALESCE(sum(quota), 0) AS total_quota, count(*) AS request_count, COALESCE(sum(prompt_tokens), 0) AS prompt_tokens, COALESCE(sum(completion_tokens), 0) AS completion_tokens").Group("channel_id").Scan(&rows).Error; err != nil {
			return nil, err
		}
		for _, row := range rows {
			item, ok := itemMap[row.ChannelId]
			if !ok {
				item = &OrganizationBillingDimension{ChannelId: row.ChannelId}
				itemMap[row.ChannelId] = item
			}
			item.TotalQuota += row.TotalQuota
			item.RequestCount += row.RequestCount
			item.PromptTokens += row.PromptTokens
			item.CompletionTokens += row.CompletionTokens
		}
	}
	items := make([]OrganizationBillingDimension, 0, len(itemMap))
	for _, item := range itemMap {
		items = append(items, *item)
	}
	fillBillingDimensionChannels(items)
	sortBillingDimensions(items)
	return items, nil
}

func fillBillingDimensionChannels(items []OrganizationBillingDimension) {
	channelIds := types.NewSet[int]()
	for _, item := range items {
		if item.ChannelId > 0 {
			channelIds.Add(item.ChannelId)
		}
	}
	if channelIds.Len() == 0 {
		return
	}
	var channels []struct {
		Id   int    `gorm:"column:id"`
		Name string `gorm:"column:name"`
	}
	if err := DB.Table("channels").Select("id, name").Where("id IN ?", channelIds.Items()).Find(&channels).Error; err != nil {
		common.SysError(fmt.Sprintf("failed to hydrate organization billing channels: %s", err.Error()))
		return
	}
	channelMap := make(map[int]string, len(channels))
	for _, channel := range channels {
		channelMap[channel.Id] = channel.Name
	}
	for i := range items {
		items[i].ChannelName = channelMap[items[i].ChannelId]
	}
}

func GetOrganizationBillingTrend(organizationId int, filters OrganizationBillingFilters) ([]OrganizationBillingTrendPoint, error) {
	members, err := activeAndHistoricalOrganizationMembers(organizationId, filters.UserId)
	if err != nil {
		return nil, err
	}
	periodExpr := organizationTrendPeriodExpr()
	selectExpr := fmt.Sprintf("%s AS period_bucket, COALESCE(sum(quota), 0) AS total_quota, count(*) AS request_count, COALESCE(sum(prompt_tokens), 0) AS prompt_tokens, COALESCE(sum(completion_tokens), 0) AS completion_tokens", periodExpr)
	pointMap := map[string]*OrganizationBillingTrendPoint{}
	for _, member := range members {
		tx, ok, err := applyOrganizationLogFilters(LOG_DB.Model(&Log{}), member, filters)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		var rows []organizationTrendAggregate
		if err := tx.Select(selectExpr).Group(periodExpr).Scan(&rows).Error; err != nil {
			return nil, err
		}
		for _, row := range rows {
			period := time.Unix(row.PeriodBucket*86400, 0).UTC().Format("2006-01-02")
			point, ok := pointMap[period]
			if !ok {
				point = &OrganizationBillingTrendPoint{Period: period}
				pointMap[period] = point
			}
			point.TotalQuota += row.TotalQuota
			point.RequestCount += row.RequestCount
			point.PromptTokens += row.PromptTokens
			point.CompletionTokens += row.CompletionTokens
		}
	}
	points := make([]OrganizationBillingTrendPoint, 0, len(pointMap))
	for _, point := range pointMap {
		points = append(points, *point)
	}
	sort.Slice(points, func(i, j int) bool {
		return points[i].Period < points[j].Period
	})
	return points, nil
}

type organizationTrendAggregate struct {
	PeriodBucket     int64 `gorm:"column:period_bucket"`
	TotalQuota       int
	RequestCount     int
	PromptTokens     int
	CompletionTokens int
}

func organizationTrendPeriodExpr() string {
	const beijingOffsetSeconds = 8 * 60 * 60
	switch common.LogDatabaseType() {
	case common.DatabaseTypeClickHouse:
		return fmt.Sprintf("intDiv(created_at + %d, 86400)", beijingOffsetSeconds)
	case common.DatabaseTypeMySQL:
		return fmt.Sprintf("FLOOR((created_at + %d) / 86400)", beijingOffsetSeconds)
	default:
		return fmt.Sprintf("(created_at + %d) / 86400", beijingOffsetSeconds)
	}
}

func GetOrganizationBillingLogs(organizationId int, filters OrganizationBillingFilters, startIdx int, num int) ([]*Log, int64, error) {
	members, err := activeAndHistoricalOrganizationMembers(organizationId, filters.UserId)
	if err != nil {
		return nil, 0, err
	}
	var total int64
	cursors := make([]organizationLogCursor, 0, len(members))
	for _, member := range members {
		tx, ok, err := applyOrganizationLogFilters(LOG_DB.Model(&Log{}), member, filters)
		if err != nil {
			return nil, 0, err
		}
		if !ok {
			continue
		}
		var count int64
		if err := tx.Count(&count).Error; err != nil {
			return nil, 0, err
		}
		total += count
		if count > 0 {
			cursors = append(cursors, organizationLogCursor{member: member})
		}
	}
	if num <= 0 || startIdx >= int(total) {
		return []*Log{}, total, nil
	}
	for i := range cursors {
		if err := cursors[i].loadMore(filters, 1); err != nil {
			return nil, 0, err
		}
	}
	batchSize := num
	if batchSize < 20 {
		batchSize = 20
	}
	page := make([]*Log, 0, num)
	for seen := 0; seen < startIdx+num; seen++ {
		bestCursorIndex := -1
		var bestLog *Log
		for i := range cursors {
			current := cursors[i].current()
			if current == nil {
				continue
			}
			if bestLog == nil || organizationLogComesBefore(current, bestLog) {
				bestLog = current
				bestCursorIndex = i
			}
		}
		if bestCursorIndex < 0 || bestLog == nil {
			break
		}
		if seen >= startIdx {
			page = append(page, bestLog)
		}
		if err := cursors[bestCursorIndex].advance(filters, batchSize); err != nil {
			return nil, 0, err
		}
	}
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		assignDisplayLogIds(page, startIdx)
	}
	hydrateLogChannelNames(page)
	fillOrganizationBillingLogUsernames(page)
	return page, total, nil
}

func StreamOrganizationBillingLogs(
	organizationId int,
	filters OrganizationBillingFilters,
	batchSize int,
	consume func([]*Log) error,
) error {
	if batchSize <= 0 {
		return errors.New("organization billing log stream batch size must be positive")
	}
	if consume == nil {
		return errors.New("organization billing log stream consumer is required")
	}
	members, err := activeAndHistoricalOrganizationMembers(organizationId, filters.UserId)
	if err != nil {
		return err
	}
	cursors := make([]organizationLogCursor, 0, len(members))
	for _, member := range members {
		cursor := organizationLogCursor{member: member}
		if err := cursor.loadMore(filters, 1); err != nil {
			return err
		}
		if cursor.current() != nil {
			cursors = append(cursors, cursor)
		}
	}

	const maxCursorBatchSize = 100
	cursorBatchSize := batchSize
	if cursorBatchSize > maxCursorBatchSize {
		cursorBatchSize = maxCursorBatchSize
	}
	batch := make([]*Log, 0, batchSize)
	streamed := 0
	for {
		bestCursorIndex := -1
		var bestLog *Log
		for i := range cursors {
			current := cursors[i].current()
			if current == nil {
				continue
			}
			if bestLog == nil || organizationLogComesBefore(current, bestLog) {
				bestLog = current
				bestCursorIndex = i
			}
		}
		if bestCursorIndex < 0 || bestLog == nil {
			break
		}
		batch = append(batch, bestLog)
		if err := cursors[bestCursorIndex].advance(filters, cursorBatchSize); err != nil {
			return err
		}
		if len(batch) < batchSize {
			continue
		}
		if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
			assignDisplayLogIds(batch, streamed)
		}
		hydrateLogChannelNames(batch)
		fillOrganizationBillingLogUsernames(batch)
		if err := consume(batch); err != nil {
			return err
		}
		streamed += len(batch)
		batch = make([]*Log, 0, batchSize)
	}
	if len(batch) == 0 {
		return nil
	}
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		assignDisplayLogIds(batch, streamed)
	}
	hydrateLogChannelNames(batch)
	fillOrganizationBillingLogUsernames(batch)
	return consume(batch)
}

type organizationLogCursor struct {
	member OrganizationMember
	rows   []*Log
	index  int
	offset int
	done   bool
}

func (c *organizationLogCursor) current() *Log {
	if c.index >= len(c.rows) {
		return nil
	}
	return c.rows[c.index]
}

func (c *organizationLogCursor) advance(filters OrganizationBillingFilters, batchSize int) error {
	c.index++
	if c.index < len(c.rows) {
		return nil
	}
	return c.loadMore(filters, batchSize)
}

func (c *organizationLogCursor) loadMore(filters OrganizationBillingFilters, limit int) error {
	if c.done {
		c.rows = nil
		c.index = 0
		return nil
	}
	rows, err := fetchOrganizationMemberLogs(c.member, filters, c.offset, limit)
	if err != nil {
		return err
	}
	c.rows = rows
	c.index = 0
	c.offset += len(rows)
	if len(rows) < limit {
		c.done = true
	}
	return nil
}

func fetchOrganizationMemberLogs(member OrganizationMember, filters OrganizationBillingFilters, offset int, limit int) ([]*Log, error) {
	tx, ok, err := applyOrganizationLogFilters(LOG_DB.Model(&Log{}), member, filters)
	if err != nil {
		return nil, err
	}
	if !ok || limit <= 0 {
		return []*Log{}, nil
	}
	var logs []*Log
	if err := tx.Order(organizationLogOrder()).Offset(offset).Limit(limit).Find(&logs).Error; err != nil {
		return nil, err
	}
	return logs, nil
}

func organizationLogOrder() string {
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		return clickHouseLogOrder("")
	}
	return "created_at desc, id desc"
}

func organizationLogComesBefore(left *Log, right *Log) bool {
	if left.CreatedAt != right.CreatedAt {
		return left.CreatedAt > right.CreatedAt
	}
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		return left.RequestId > right.RequestId
	}
	return left.Id > right.Id
}

func hydrateLogChannelNames(logs []*Log) {
	channelIds := types.NewSet[int]()
	for _, log := range logs {
		if log.ChannelId > 0 {
			channelIds.Add(log.ChannelId)
		}
	}
	if channelIds.Len() == 0 {
		return
	}
	var channels []struct {
		Id   int    `gorm:"column:id"`
		Name string `gorm:"column:name"`
	}
	if err := DB.Table("channels").Select("id, name").Where("id IN ?", channelIds.Items()).Find(&channels).Error; err != nil {
		common.SysError(fmt.Sprintf("failed to hydrate organization billing log channels: %s", err.Error()))
		return
	}
	channelMap := make(map[int]string, len(channels))
	for _, channel := range channels {
		channelMap[channel.Id] = channel.Name
	}
	for i := range logs {
		logs[i].ChannelName = channelMap[logs[i].ChannelId]
	}
}

func fillOrganizationBillingLogUsernames(logs []*Log) {
	for _, log := range logs {
		log.Username = OrganizationBillingUsername(log.Username, log.UserId)
	}
}

// OrganizationBillingStartPreview 描述把某成员的账单归属起点设为候选值后的预览结果：
// 相对当前生效窗口新增纳入的日志统计、候选窗口内的日志时间范围、该用户日志库最早保留
// 时间，以及候选窗口是否与同组织其他成员记录冲突。
type OrganizationBillingStartPreview struct {
	MemberId              int   `json:"member_id"`
	OrganizationId        int   `json:"organization_id"`
	UserId                int   `json:"user_id"`
	JoinedAt              int64 `json:"joined_at"`
	CurrentBillingStart   int64 `json:"current_billing_start"`
	CandidateBillingStart int64 `json:"candidate_billing_start"`
	EarliestLogAt         int64 `json:"earliest_log_at"`
	LatestLogAt           int64 `json:"latest_log_at"`
	EarliestRetainedAt    int64 `json:"earliest_retained_at"`
	AddedRequestCount     int   `json:"added_request_count"`
	AddedQuota            int   `json:"added_quota"`
	AddedPromptTokens     int   `json:"added_prompt_tokens"`
	AddedCompletionTokens int   `json:"added_completion_tokens"`
	Conflict              bool  `json:"conflict"`
}

type organizationBillingStartPreviewRow struct {
	TotalQuota       int
	RequestCount     int
	PromptTokens     int
	CompletionTokens int
	Earliest         int64 `gorm:"column:earliest"`
	Latest           int64 `gorm:"column:latest"`
}

// currentOrganizationMember 读取某用户在某组织的当前在职成员记录（left_at = 0）。
func currentOrganizationMember(organizationId, userId int) (OrganizationMember, error) {
	var member OrganizationMember
	if err := DB.Where("organization_id = ? AND user_id = ? AND left_at = 0", organizationId, userId).
		First(&member).Error; err != nil {
		return OrganizationMember{}, err
	}
	return member, nil
}

// validateCandidateBillingStart 校验候选账单起点的基本业务规则（不含窗口重叠校验）。
// 返回该记录当前的生效起点，供调用方构造差集窗口与乐观锁预期值。
//
// 上界收紧为 currentEffective（≤ JoinedAt）：本能力只允许向前补历史，禁止把已回填的
// 起点调晚以从报表中截断已纳入的消费——否则预览会因 candidate >= currentEffective 而
// 返回 Added=0，却实际移除历史，造成"预览零影响、应用后账单缩小"的不一致。
func validateCandidateBillingStart(member OrganizationMember, candidate int64) (int64, error) {
	currentEffective := effectiveBillingStart(member)
	if candidate <= 0 {
		return 0, errors.New("billing_start_at must be greater than zero")
	}
	if candidate > currentEffective {
		return 0, errors.New("billing_start_at must not be later than the current billing start")
	}
	if candidate > common.GetTimestamp() {
		return 0, errors.New("billing_start_at must not be in the future")
	}
	return currentEffective, nil
}

// intervalsOverlap 判断两个半开区间 [s1, l1) 与 [s2, l2) 是否相交，l==0 表示开向 +∞。
func intervalsOverlap(s1, l1, s2, l2 int64) bool {
	if l1 > 0 && l1 <= s2 {
		return false
	}
	if l2 > 0 && l2 <= s1 {
		return false
	}
	return true
}

// billingWindowOverlaps 检查候选窗口 [candidateStart, candidateLeft) 是否与该用户在同组织内
// 的其他成员记录（排除 excludeMemberId）账单窗口相交。窗口不相交是防止同一条日志在同组织内
// 被两段成员关系重复统计的不变量；跨组织窗口允许重叠，此处不做跨组织校验。
func billingWindowOverlaps(tx *gorm.DB, organizationId, userId, excludeMemberId int, candidateStart, candidateLeft int64) (bool, error) {
	var others []OrganizationMember
	if err := tx.Where("organization_id = ? AND user_id = ? AND id <> ?", organizationId, userId, excludeMemberId).
		Find(&others).Error; err != nil {
		return false, err
	}
	for _, other := range others {
		if intervalsOverlap(candidateStart, candidateLeft, effectiveBillingStart(other), other.LeftAt) {
			return true, nil
		}
	}
	return false, nil
}

// previewAddedBillingRange 统计把 member.BillingStartAt 设为 candidate 后、相对当前生效起点
// 新增纳入的日志：即落在 [candidate, currentEffective) 区间内的日志。预览固定为全消费口径
// （与 Summary 默认一致），不接受外部 type/model/channel 筛选——更新会全局改变账单窗口，
// 预览口径必须与之统一，否则预览值与实际影响漂移。
func previewAddedBillingRange(member OrganizationMember, candidate, currentEffective int64) (organizationLogAggregate, int64, int64, error) {
	if candidate <= 0 || candidate >= currentEffective {
		return organizationLogAggregate{}, 0, 0, nil
	}
	previewMember := member
	previewMember.BillingStartAt = candidate
	previewFilters := OrganizationBillingFilters{
		StartTimestamp: candidate,
		EndTimestamp:   currentEffective - 1,
	}
	tx, ok, err := applyOrganizationLogFilters(LOG_DB.Model(&Log{}), previewMember, previewFilters)
	if err != nil {
		return organizationLogAggregate{}, 0, 0, err
	}
	if !ok {
		return organizationLogAggregate{}, 0, 0, nil
	}
	var row organizationBillingStartPreviewRow
	if err := tx.Select("COALESCE(sum(quota), 0) AS total_quota, count(*) AS request_count, COALESCE(sum(prompt_tokens), 0) AS prompt_tokens, COALESCE(sum(completion_tokens), 0) AS completion_tokens, COALESCE(min(created_at), 0) AS earliest, COALESCE(max(created_at), 0) AS latest").Scan(&row).Error; err != nil {
		return organizationLogAggregate{}, 0, 0, err
	}
	return organizationLogAggregate{
		TotalQuota:       row.TotalQuota,
		RequestCount:     row.RequestCount,
		PromptTokens:     row.PromptTokens,
		CompletionTokens: row.CompletionTokens,
	}, row.Earliest, row.Latest, nil
}

// earliestRetainedLogAt 返回某用户在日志库中最早一条日志的时间，供前端提示"最早可回填到哪"。
// 无任何日志时返回 0；查询失败时返回错误，调用方必须显式处理，避免把"查询失败"伪装成"无历史"。
func earliestRetainedLogAt(userId int) (int64, error) {
	var earliest int64
	err := LOG_DB.Model(&Log{}).Where("user_id = ?", userId).
		Select("COALESCE(min(created_at), 0)").Scan(&earliest).Error
	return earliest, err
}

// PreviewOrganizationMemberBillingStart 预览把某当前在职成员的 BillingStartAt 设为 candidate
// 后相对当前窗口新增纳入的日志统计，以及是否与同组织其他成员窗口冲突。预览只读、不加锁，
// 口径固定为全消费（不受查询参数影响）。
func PreviewOrganizationMemberBillingStart(organizationId, userId int, candidate int64) (*OrganizationBillingStartPreview, error) {
	member, err := currentOrganizationMember(organizationId, userId)
	if err != nil {
		return nil, err
	}
	currentEffective, err := validateCandidateBillingStart(member, candidate)
	if err != nil {
		return nil, err
	}
	added, earliest, latest, err := previewAddedBillingRange(member, candidate, currentEffective)
	if err != nil {
		return nil, err
	}
	conflict, err := billingWindowOverlaps(DB, organizationId, userId, member.Id, candidate, member.LeftAt)
	if err != nil {
		return nil, err
	}
	earliestRetained, err := earliestRetainedLogAt(userId)
	if err != nil {
		return nil, err
	}
	return &OrganizationBillingStartPreview{
		MemberId:              member.Id,
		OrganizationId:        organizationId,
		UserId:                member.UserId,
		JoinedAt:              member.JoinedAt,
		CurrentBillingStart:   currentEffective,
		CandidateBillingStart: candidate,
		EarliestLogAt:         earliest,
		LatestLogAt:           latest,
		EarliestRetainedAt:    earliestRetained,
		AddedRequestCount:     added.RequestCount,
		AddedQuota:            added.TotalQuota,
		AddedPromptTokens:     added.PromptTokens,
		AddedCompletionTokens: added.CompletionTokens,
		Conflict:              conflict,
	}, nil
}

// PreviewOrganizationBillingStartBatch 批量预览多个成员的候选起点，供管理员一次性评估回填影响。
func PreviewOrganizationBillingStartBatch(organizationId int, candidates map[int]int64) ([]OrganizationBillingStartPreview, error) {
	results := make([]OrganizationBillingStartPreview, 0, len(candidates))
	for userId, candidate := range candidates {
		preview, err := PreviewOrganizationMemberBillingStart(organizationId, userId, candidate)
		if err != nil {
			return nil, err
		}
		results = append(results, *preview)
	}
	sort.Slice(results, func(i, j int) bool {
		return results[i].UserId < results[j].UserId
	})
	return results, nil
}

// OrganizationBillingStartUpdateResult 描述一次归属起点更新的结果：是否实际变更（幂等指标）
// 与服务端权威计算的增量统计。增量由服务端在更新前重算，用于审计，不信任客户端回传。
type OrganizationBillingStartUpdateResult struct {
	Member                OrganizationMember
	Changed               bool
	AddedRequestCount     int
	AddedQuota            int
	AddedPromptTokens     int
	AddedCompletionTokens int
	EarliestLogAt         int64
	LatestLogAt           int64
}

// UpdateOrganizationMemberBillingStart 在事务内更新某当前在职成员的账单归属起点。
// expectedBillingStart 为乐观锁预期值，必须等于更新前的生效起点；candidate 在事务内重新
// 校验（不得晚于 joined_at、不得未来、同组织窗口不相交）。增量由服务端在更新前重算并随
// 结果返回；相同目标值重复应用时 Changed=false（不产生新数据）。
func UpdateOrganizationMemberBillingStart(organizationId, userId int, candidate, expectedBillingStart int64) (*OrganizationBillingStartUpdateResult, error) {
	result := &OrganizationBillingStartUpdateResult{}
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := lockOrganizationForMembershipChange(tx, organizationId); err != nil {
			return err
		}
		var updated OrganizationMember
		if err := lockForUpdate(tx).Where("organization_id = ? AND user_id = ? AND left_at = 0", organizationId, userId).
			First(&updated).Error; err != nil {
			return err
		}
		oldEffective := effectiveBillingStart(updated)
		if oldEffective != expectedBillingStart {
			return errors.New("billing_start_at was changed by another operation, please retry")
		}
		if _, err := validateCandidateBillingStart(updated, candidate); err != nil {
			return err
		}
		conflict, err := billingWindowOverlaps(tx, organizationId, userId, updated.Id, candidate, updated.LeftAt)
		if err != nil {
			return err
		}
		if conflict {
			return errors.New("billing window overlaps existing membership record")
		}
		// 服务端权威计算增量：差集 [candidate, oldEffective) 内的消费日志。必须在更新前计算
		// （oldEffective 为旧生效起点），口径与预览/汇总一致。
		added, earliest, latest, err := previewAddedBillingRange(updated, candidate, oldEffective)
		if err != nil {
			return err
		}
		result.AddedRequestCount = added.RequestCount
		result.AddedQuota = added.TotalQuota
		result.AddedPromptTokens = added.PromptTokens
		result.AddedCompletionTokens = added.CompletionTokens
		result.EarliestLogAt = earliest
		result.LatestLogAt = latest
		result.Changed = updated.BillingStartAt != candidate
		if !result.Changed {
			result.Member = updated
			return nil
		}
		if err := tx.Model(&OrganizationMember{}).Where("id = ?", updated.Id).Update("billing_start_at", candidate).Error; err != nil {
			return err
		}
		updated.BillingStartAt = candidate
		result.Member = updated
		return nil
	})
	if err != nil {
		return nil, err
	}
	fillOrganizationMemberUsersInPlace(&result.Member)
	return result, nil
}

func sortBillingDimensions(items []OrganizationBillingDimension) {
	sort.Slice(items, func(i, j int) bool {
		if items[i].TotalQuota == items[j].TotalQuota {
			if items[i].RequestCount == items[j].RequestCount {
				if items[i].UserId != items[j].UserId {
					return items[i].UserId < items[j].UserId
				}
				if items[i].ModelName != items[j].ModelName {
					return items[i].ModelName < items[j].ModelName
				}
				return items[i].ChannelId < items[j].ChannelId
			}
			return items[i].RequestCount > items[j].RequestCount
		}
		return items[i].TotalQuota > items[j].TotalQuota
	})
}

func fillOrganizationMemberUsersInPlace(member *OrganizationMember) {
	if member == nil {
		return
	}
	members := []OrganizationMember{*member}
	fillOrganizationMemberUsers(members)
	*member = members[0]
}
