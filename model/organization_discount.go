package model

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/QuantumNous/new-api/common"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

const (
	// OrganizationDiscountRatioScale 折扣定点缩放基数：0.8 -> 800000
	OrganizationDiscountRatioScale = 1_000_000
	// OrganizationDiscountMaxRatioScaled 组织渠道倍率严格小于 10，六位小数下最大为 9.999999。
	OrganizationDiscountMaxRatioScaled = 10*OrganizationDiscountRatioScale - 1
	// organizationDiscountMaxJSONBytes 为 MySQL TEXT 的 65,535 字节上限，
	// 超限时在保存前拒绝，不依赖数据库截断。
	organizationDiscountMaxJSONBytes = 65_535
)

var (
	ErrOrganizationDiscountInvalidRatio         = errors.New("organization discount ratio must be in (0, 10)")
	ErrOrganizationDiscountInvalidPrecision     = errors.New("organization discount ratio supports at most 6 decimal places")
	ErrOrganizationDiscountDuplicateChannel     = errors.New("duplicate channel in organization discount")
	ErrOrganizationDiscountInvalidChannel       = errors.New("invalid channel id in organization discount")
	ErrOrganizationDiscountOrganizationNotFound = errors.New("organization not found")
	ErrOrganizationDiscountSnapshotConflict     = errors.New("organization discount snapshot conflict")
	ErrOrganizationDiscountJSONTooLarge         = errors.New("organization discount payload exceeds storage limit")
	ErrOrganizationDiscountInvalidJSON          = errors.New("organization discount snapshot data is corrupted")
)

// OrganizationDiscountSnapshot 是组织的不可变渠道折扣快照。
// ChannelDiscounts 以跨数据库兼容的 TEXT 保存 JSON 映射，键为渠道 ID 字符串，
// 值为定点整数倍率；任何修改只能追加新快照。
type OrganizationDiscountSnapshot struct {
	Id               int    `json:"id"`
	OrganizationId   int    `json:"organization_id" gorm:"index"`
	ChannelDiscounts string `json:"channel_discounts" gorm:"type:text;not null"`
	CreatedBy        int    `json:"created_by"`
	CreatedAt        int64  `json:"created_at" gorm:"autoCreateTime"`
}

func (OrganizationDiscountSnapshot) TableName() string {
	return "organization_discount_snapshots"
}

// ParseOrganizationDiscountRatio 把 "0.85" 解析为定点整数 850000。
// 输入必须可精确转换到最多六位小数，超精度输入报错而不是静默截断；
// 范围校验在 decimal 上先于整数转换执行，避免超大值窄化后落入合法区间。
func ParseOrganizationDiscountRatio(value string) (int, error) {
	d, err := decimal.NewFromString(value)
	if err != nil {
		return 0, fmt.Errorf("invalid discount ratio %q: %w", value, err)
	}
	scaled := d.Mul(decimal.NewFromInt(OrganizationDiscountRatioScale))
	if !scaled.IsInteger() {
		return 0, fmt.Errorf("%w: %q", ErrOrganizationDiscountInvalidPrecision, value)
	}
	maxScaled := decimal.NewFromInt(OrganizationDiscountMaxRatioScaled)
	if !scaled.IsPositive() || scaled.GreaterThan(maxScaled) {
		return 0, fmt.Errorf("%w: %q", ErrOrganizationDiscountInvalidRatio, value)
	}
	return int(scaled.IntPart()), nil
}

// FormatOrganizationDiscountRatio 把 850000 格式化为 "0.85"
func FormatOrganizationDiscountRatio(scaled int) string {
	return decimal.NewFromInt(int64(scaled)).Div(decimal.NewFromInt(OrganizationDiscountRatioScale)).String()
}

// OrganizationDiscountRatioFloatFromScaled 把定点整数转为 float64（计费乘数用）
func OrganizationDiscountRatioFloatFromScaled(scaled int) float64 {
	return float64(scaled) / float64(OrganizationDiscountRatioScale)
}

// ValidateOrganizationDiscountRatioScaled 校验组织渠道倍率：0 < ratio < 10。
func ValidateOrganizationDiscountRatioScaled(scaled int) error {
	if scaled <= 0 || scaled > OrganizationDiscountMaxRatioScaled {
		return fmt.Errorf("%w: scaled value %d out of range", ErrOrganizationDiscountInvalidRatio, scaled)
	}
	return nil
}

// MarshalOrganizationChannelDiscounts 把渠道折扣映射序列化为 TEXT JSON。
// 1.0 的倍率在保存时归一化为未配置（直接从入参剔除），空映射序列化为 "{}"。
func MarshalOrganizationChannelDiscounts(discounts map[int]int) (string, error) {
	normalized := make(map[string]int, len(discounts))
	for channelId, ratioScaled := range discounts {
		if ratioScaled == OrganizationDiscountRatioScale {
			continue
		}
		normalized[fmt.Sprintf("%d", channelId)] = ratioScaled
	}
	data, err := common.Marshal(normalized)
	if err != nil {
		return "", err
	}
	if len(data) > organizationDiscountMaxJSONBytes {
		return "", ErrOrganizationDiscountJSONTooLarge
	}
	return string(data), nil
}

// UnmarshalOrganizationChannelDiscounts 解析快照 TEXT JSON；损坏数据返回错误（失败关闭）。
// 值域校验收敛在此单一边界：合法 JSON 中的非法倍率同样视为损坏，不得静默按 1.0 计费。
func UnmarshalOrganizationChannelDiscounts(data string) (map[int]int, error) {
	normalized := make(map[string]int)
	if err := common.UnmarshalJsonStr(data, &normalized); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrOrganizationDiscountInvalidJSON, err.Error())
	}
	discounts := make(map[int]int, len(normalized))
	for key, ratioScaled := range normalized {
		channelId, err := strconv.Atoi(key)
		if err != nil || channelId <= 0 {
			return nil, fmt.Errorf("%w: channel key %q", ErrOrganizationDiscountInvalidJSON, key)
		}
		if ratioScaled <= 0 || ratioScaled > OrganizationDiscountMaxRatioScaled {
			return nil, fmt.Errorf("%w: channel %d ratio %d", ErrOrganizationDiscountInvalidJSON, channelId, ratioScaled)
		}
		discounts[channelId] = ratioScaled
	}
	return discounts, nil
}

// OrganizationChannelDiscountParam 单个渠道折扣入参
type OrganizationChannelDiscountParam struct {
	ChannelId   int
	RatioScaled int
}

// OrganizationDiscountChannelOption 折扣配置页的渠道选项（轻量 id/name）。
// 渠道是全局资源而非账单聚合结果：新渠道未产生消费前也必须可配置折扣。
type OrganizationDiscountChannelOption struct {
	Id     int    `json:"id"`
	Name   string `json:"name"`
	Status int    `json:"status"`
}

// ListOrganizationDiscountChannelOptions 返回全部系统渠道的 id/name/status，
// 按渠道 ID 升序。供折扣专用 Root 接口使用，不依赖消费日志聚合。
func ListOrganizationDiscountChannelOptions() ([]OrganizationDiscountChannelOption, error) {
	var options []OrganizationDiscountChannelOption
	err := DB.Model(&Channel{}).
		Select("id, name, status").
		Order("id ASC").
		Find(&options).Error
	return options, err
}

// SaveOrganizationDiscountParams 保存组织折扣参数
type SaveOrganizationDiscountParams struct {
	OrganizationId     int
	ChannelDiscounts   []OrganizationChannelDiscountParam
	ExpectedSnapshotId int
	CreatedBy          int
}

// SaveOrganizationDiscount 以完整替换语义保存组织折扣：校验后在同一事务内
// 追加新快照，并用快照 ID 条件更新组织当前指针（乐观锁）。
// RowsAffected != 1 时事务回滚（失败快照不残留）并返回快照冲突错误。
func SaveOrganizationDiscount(params SaveOrganizationDiscountParams) (*OrganizationDiscountSnapshot, error) {
	if _, err := GetOrganizationById(params.OrganizationId); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrOrganizationDiscountOrganizationNotFound
		}
		return nil, err
	}
	if params.ExpectedSnapshotId < 0 {
		return nil, fmt.Errorf("%w: expected_snapshot_id must be non-negative", ErrOrganizationDiscountSnapshotConflict)
	}

	discounts := make(map[int]int, len(params.ChannelDiscounts))
	channelIds := make([]int, 0, len(params.ChannelDiscounts))
	for _, item := range params.ChannelDiscounts {
		if item.ChannelId <= 0 {
			return nil, ErrOrganizationDiscountInvalidChannel
		}
		if _, ok := discounts[item.ChannelId]; ok {
			return nil, ErrOrganizationDiscountDuplicateChannel
		}
		if err := ValidateOrganizationDiscountRatioScaled(item.RatioScaled); err != nil {
			return nil, err
		}
		discounts[item.ChannelId] = item.RatioScaled
		channelIds = append(channelIds, item.ChannelId)
	}

	snapshot := &OrganizationDiscountSnapshot{
		OrganizationId: params.OrganizationId,
		CreatedBy:      params.CreatedBy,
	}

	err := DB.Transaction(func(tx *gorm.DB) error {
		if len(channelIds) > 0 {
			var channelCount int64
			if err := tx.Model(&Channel{}).Where("id IN ?", channelIds).Count(&channelCount).Error; err != nil {
				return err
			}
			if channelCount != int64(len(channelIds)) {
				return ErrOrganizationDiscountInvalidChannel
			}
		}

		data, err := MarshalOrganizationChannelDiscounts(discounts)
		if err != nil {
			return err
		}
		snapshot.ChannelDiscounts = data
		if err := tx.Create(snapshot).Error; err != nil {
			return err
		}

		// 条件更新当前指针：仅当组织仍指向 expectedSnapshotId 时生效。
		// 不依赖各数据库不同的重复键错误，只以 RowsAffected 判定冲突。
		result := tx.Model(&Organization{}).
			Where("id = ? AND current_discount_snapshot_id = ?", params.OrganizationId, params.ExpectedSnapshotId).
			Update("current_discount_snapshot_id", snapshot.Id)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("%w: expected snapshot %d is no longer current", ErrOrganizationDiscountSnapshotConflict, params.ExpectedSnapshotId)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return snapshot, nil
}

// GetCurrentOrganizationDiscountSnapshot 取组织当前折扣快照。
// 以 Organization.CurrentDiscountSnapshotId 为唯一权威；无快照时返回 nil, nil。
func GetCurrentOrganizationDiscountSnapshot(organizationId int) (*OrganizationDiscountSnapshot, error) {
	if organizationId <= 0 {
		return nil, nil
	}
	var snapshot OrganizationDiscountSnapshot
	err := DB.Table("organization_discount_snapshots AS snapshots").
		Select("snapshots.*").
		Joins("JOIN organizations AS organizations ON organizations.current_discount_snapshot_id = snapshots.id").
		Where("organizations.id = ?", organizationId).
		Take(&snapshot).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &snapshot, nil
}

// GetCurrentOrganizationDiscountSnapshotForUser 取用户当前组织的折扣快照。
// 用户未加入组织或组织无快照时返回 nil, nil；数据库错误返回错误。
func GetCurrentOrganizationDiscountSnapshotForUser(userId int) (*OrganizationDiscountSnapshot, error) {
	if userId <= 0 {
		return nil, nil
	}
	orgWithMember, err := GetCurrentOrganizationForUser(userId)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if orgWithMember == nil {
		return nil, nil
	}
	return GetCurrentOrganizationDiscountSnapshot(orgWithMember.Organization.Id)
}

// OrganizationDiscountChange 描述一份快照相对上一份快照的单一渠道变更。
// OldScaled/NewScaled 为 0 表示该侧未配置。
type OrganizationDiscountChange struct {
	ChannelId int
	OldScaled int
	NewScaled int
}

// OrganizationDiscountHistoryItem 单条历史记录：不可变快照 + 派生变更
type OrganizationDiscountHistoryItem struct {
	Snapshot      OrganizationDiscountSnapshot
	CreatedByName string
	Discounts     map[int]int
	Changes       []OrganizationDiscountChange
}

// OrganizationDiscountHistoryPage 历史分页结果
type OrganizationDiscountHistoryPage struct {
	Items []OrganizationDiscountHistoryItem
	Total int64
}

// GetOrganizationDiscountHistory 按 Id 倒序分页返回组织折扣历史。
// 每份快照与同组织上一份快照比较后派生变更；分页会额外读取当前页最旧
// 记录的上一份快照，避免把分页边界误判为空配置。只有组织的第一份快照
// 以空配置作为变更前状态。
func GetOrganizationDiscountHistory(organizationId int, startIdx int, num int) (*OrganizationDiscountHistoryPage, error) {
	var total int64
	if err := DB.Model(&OrganizationDiscountSnapshot{}).
		Where("organization_id = ?", organizationId).
		Count(&total).Error; err != nil {
		return nil, err
	}
	page := &OrganizationDiscountHistoryPage{Total: total}
	if num <= 0 || startIdx >= int(total) {
		page.Items = []OrganizationDiscountHistoryItem{}
		return page, nil
	}

	type snapshotWithOperator struct {
		OrganizationDiscountSnapshot
		CreatedByName string `gorm:"column:created_by_name"`
	}
	var snapshots []snapshotWithOperator
	if err := DB.Table("organization_discount_snapshots AS snapshots").
		Select("snapshots.*, COALESCE(NULLIF(users.display_name, ''), users.username) AS created_by_name").
		Joins("LEFT JOIN users AS users ON users.id = snapshots.created_by").
		Where("snapshots.organization_id = ?", organizationId).
		Order("snapshots.id DESC").
		Offset(startIdx).Limit(num).
		Find(&snapshots).Error; err != nil {
		return nil, err
	}

	previousScaled := map[int]int{}
	if len(snapshots) > 0 {
		oldestId := snapshots[len(snapshots)-1].Id
		var previous OrganizationDiscountSnapshot
		err := DB.Where("organization_id = ? AND id < ?", organizationId, oldestId).
			Order("id DESC").
			First(&previous).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		if err == nil {
			previousScaled, err = UnmarshalOrganizationChannelDiscounts(previous.ChannelDiscounts)
			if err != nil {
				return nil, err
			}
		}
	}

	page.Items = make([]OrganizationDiscountHistoryItem, 0, len(snapshots))
	for i := len(snapshots) - 1; i >= 0; i-- {
		item := OrganizationDiscountHistoryItem{
			Snapshot:      snapshots[i].OrganizationDiscountSnapshot,
			CreatedByName: snapshots[i].CreatedByName,
		}
		currentScaled, err := UnmarshalOrganizationChannelDiscounts(snapshots[i].ChannelDiscounts)
		if err != nil {
			return nil, err
		}
		item.Discounts = currentScaled
		item.Changes = diffOrganizationChannelDiscounts(previousScaled, currentScaled)
		page.Items = append(page.Items, item)
		previousScaled = currentScaled
	}
	// 页内按 Id 倒序输出，与分页方向一致。
	for i, j := 0, len(page.Items)-1; i < j; i, j = i+1, j-1 {
		page.Items[i], page.Items[j] = page.Items[j], page.Items[i]
	}
	return page, nil
}

// diffOrganizationChannelDiscounts 派生 current 相对 previous 的渠道级变更，
// 输出按渠道 ID 升序，保证历史展示稳定。
func diffOrganizationChannelDiscounts(previous map[int]int, current map[int]int) []OrganizationDiscountChange {
	channelIds := make(map[int]struct{}, len(previous)+len(current))
	for channelId := range previous {
		channelIds[channelId] = struct{}{}
	}
	for channelId := range current {
		channelIds[channelId] = struct{}{}
	}
	changes := make([]OrganizationDiscountChange, 0, len(channelIds))
	for channelId := range channelIds {
		oldScaled := previous[channelId]
		newScaled := current[channelId]
		if oldScaled == newScaled {
			continue
		}
		changes = append(changes, OrganizationDiscountChange{
			ChannelId: channelId,
			OldScaled: oldScaled,
			NewScaled: newScaled,
		})
	}
	for i := 1; i < len(changes); i++ {
		for j := i; j > 0 && changes[j].ChannelId < changes[j-1].ChannelId; j-- {
			changes[j], changes[j-1] = changes[j-1], changes[j]
		}
	}
	return changes
}
