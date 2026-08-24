package service

import (
	"errors"
	"fmt"

	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	hosttypes "github.com/QuantumNous/new-api/types"

	"github.com/shopspring/decimal"
)

var (
	// ErrOrganizationDiscountLoadFailed indicates a database or parse failure
	// when resolving the organization discount snapshot. Callers must treat it
	// as a 500-class error; a request may never degrade to a default 1.0.
	ErrOrganizationDiscountLoadFailed = errors.New("failed to load organization discount snapshot")
)

// LoadOrganizationDiscountSnapshot 根据用户当前组织一次性加载完整折扣快照。
// 无组织、无快照返回 nil, nil；数据库或解析错误统一分类为加载失败（失败关闭）。
func LoadOrganizationDiscountSnapshot(userId int) (*hosttypes.OrganizationDiscountSnapshot, error) {
	if userId <= 0 {
		return nil, nil
	}
	snapshot, err := model.GetCurrentOrganizationDiscountSnapshotForUser(userId)
	if err != nil {
		return nil, fmt.Errorf("%w for user %d: %w", ErrOrganizationDiscountLoadFailed, userId, err)
	}
	if snapshot == nil {
		return nil, nil
	}
	discounts, err := model.UnmarshalOrganizationChannelDiscounts(snapshot.ChannelDiscounts)
	if err != nil {
		return nil, fmt.Errorf("%w for snapshot %d: %w", ErrOrganizationDiscountLoadFailed, snapshot.Id, err)
	}
	channelDiscounts := make(map[int]float64, len(discounts))
	for channelId, ratioScaled := range discounts {
		channelDiscounts[channelId] = model.OrganizationDiscountRatioFloatFromScaled(ratioScaled)
	}
	return &hosttypes.OrganizationDiscountSnapshot{
		SnapshotID:       snapshot.Id,
		ChannelDiscounts: channelDiscounts,
	}, nil
}

// ResolveOrganizationDiscountSnapshotForChannel 为按次任务解析请求固定折扣并钉住当前渠道。
// 已解析的请求只复用原内存映射，重试切换渠道时不会重新读取组织当前快照。
func ResolveOrganizationDiscountSnapshotForChannel(priceData hosttypes.PriceData, userId, channelId int) (*hosttypes.OrganizationDiscountSnapshot, error) {
	var snapshot *hosttypes.OrganizationDiscountSnapshot
	if priceData.DiscountSnapshotLoaded {
		if priceData.DiscountSnapshot == nil {
			return nil, nil
		}
		fixedSnapshot := *priceData.DiscountSnapshot
		snapshot = &fixedSnapshot
	} else {
		var err error
		snapshot, err = LoadOrganizationDiscountSnapshot(userId)
		if err != nil {
			return nil, err
		}
	}
	if snapshot == nil {
		return nil, nil
	}
	snapshot.PinChannel(channelId)
	return snapshot, nil
}

// ApplyOrganizationDiscountForChannel 在渠道确定后把实际渠道折扣钉在请求内存快照上。
// 只查同一内存映射；未配置渠道为 1.0，不访问数据库，因此没有错误路径。
// 组织折扣不大于 1.0，渠道确定后的目标费用不会超过未打折预扣额，无需补扣。
func ApplyOrganizationDiscountForChannel(relayInfo *relaycommon.RelayInfo, channelId int) {
	snapshot := relayInfo.PriceData.DiscountSnapshot
	if snapshot == nil {
		return
	}
	snapshot.PinChannel(channelId)
}

// organizationDiscountMultiplier 返回实际应用折扣的 decimal 乘数（无折扣时为 1）。
func organizationDiscountMultiplier(priceData hosttypes.PriceData) decimal.Decimal {
	return decimal.NewFromFloat(priceData.DiscountSnapshot.EffectiveRatio())
}

// organizationDiscountRatioFloat 返回实际应用折扣（无折扣时为 1.0）。
func organizationDiscountRatioFloat(priceData hosttypes.PriceData) float64 {
	return priceData.DiscountSnapshot.EffectiveRatio()
}
