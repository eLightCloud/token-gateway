package types

import (
	"fmt"
	"math"

	"github.com/shopspring/decimal"
)

type GroupRatioInfo struct {
	GroupRatio        float64
	GroupSpecialRatio float64
	HasSpecialRatio   bool
}

// OrganizationDiscountSnapshot 在请求首次计费解析时固定组织渠道折扣。
// ChannelDiscounts 是完整内存映射，渠道确定后只从中查找，不再访问数据库。
type OrganizationDiscountSnapshot struct {
	SnapshotID       int             `json:"snapshot_id"`
	ChannelDiscounts map[int]float64 `json:"-"`
	AppliedChannelId int             `json:"channel_id,omitempty"`
	AppliedRatio     float64         `json:"ratio,omitempty"`
}

// RatioForChannel 从内存快照映射中查找渠道折扣；未配置返回 1.0。
func (s *OrganizationDiscountSnapshot) RatioForChannel(channelId int) float64 {
	if s == nil || channelId <= 0 {
		return 1.0
	}
	if ratio, ok := s.ChannelDiscounts[channelId]; ok {
		return ratio
	}
	return 1.0
}

// PinChannel records the only channel-specific discount fact used by billing.
func (s *OrganizationDiscountSnapshot) PinChannel(channelId int) {
	if s == nil {
		return
	}
	s.AppliedChannelId = channelId
	s.AppliedRatio = s.RatioForChannel(channelId)
}

// EffectiveRatio 返回实际应用的渠道折扣；未选择渠道时为 1.0。
func (s *OrganizationDiscountSnapshot) EffectiveRatio() float64 {
	if s == nil || s.AppliedRatio <= 0 {
		return 1.0
	}
	return s.AppliedRatio
}

type PriceData struct {
	FreeModel            bool
	ModelPrice           float64
	ModelRatio           float64
	CompletionRatio      float64
	CacheRatio           float64
	CacheCreationRatio   float64
	CacheCreation5mRatio float64
	CacheCreation1hRatio float64
	ImageRatio           float64
	AudioRatio           float64
	AudioCompletionRatio float64
	otherRatios          map[string]float64
	UsePrice             bool
	Quota                int // 按次计费的折后最终额度（MJ / Task）
	QuotaToPreConsume    int // 未应用组织折扣的准入/预扣额度
	GroupRatioInfo       GroupRatioInfo
	DiscountSnapshot     *OrganizationDiscountSnapshot
	// DiscountSnapshotLoaded distinguishes "not resolved" from "resolved with no
	// discount". Billing paths must never reload a resolved request.
	DiscountSnapshotLoaded bool
}

func (p *PriceData) AddOtherRatio(key string, ratio float64) {
	if !isValidOtherRatio(ratio) {
		return
	}
	if p.otherRatios == nil {
		p.otherRatios = make(map[string]float64)
	}
	p.otherRatios[key] = ratio
}

func (p *PriceData) ReplaceOtherRatios(ratios map[string]float64) bool {
	p.otherRatios = nil
	for key, ratio := range ratios {
		p.AddOtherRatio(key, ratio)
	}
	return len(p.otherRatios) > 0
}

func (p *PriceData) HasOtherRatio(key string) bool {
	ratio, ok := p.otherRatios[key]
	return ok && isValidOtherRatio(ratio)
}

func (p *PriceData) OtherRatios() map[string]float64 {
	if len(p.otherRatios) == 0 {
		return nil
	}
	ratios := make(map[string]float64, len(p.otherRatios))
	for key, ratio := range p.otherRatios {
		if isValidOtherRatio(ratio) {
			ratios[key] = ratio
		}
	}
	if len(ratios) == 0 {
		return nil
	}
	return ratios
}

func (p *PriceData) OtherRatioMultiplier() float64 {
	multiplier := 1.0
	for _, ratio := range p.otherRatios {
		if isValidOtherRatio(ratio) && ratio != 1.0 {
			multiplier *= ratio
		}
	}
	return multiplier
}

func (p *PriceData) ApplyOtherRatiosToFloat(value float64) float64 {
	return value * p.OtherRatioMultiplier()
}

func (p *PriceData) ApplyOtherRatiosToDecimal(value decimal.Decimal) decimal.Decimal {
	for _, ratio := range p.otherRatios {
		if isValidOtherRatio(ratio) && ratio != 1.0 {
			value = value.Mul(decimal.NewFromFloat(ratio))
		}
	}
	return value
}

func (p *PriceData) RemoveOtherRatiosFromFloat(value float64) float64 {
	for _, ratio := range p.otherRatios {
		if isValidOtherRatio(ratio) && ratio != 1.0 {
			value /= ratio
		}
	}
	return value
}

func isValidOtherRatio(ratio float64) bool {
	return ratio > 0 && !math.IsInf(ratio, 1)
}

func (p *PriceData) ToSetting() string {
	return fmt.Sprintf("ModelPrice: %f, ModelRatio: %f, CompletionRatio: %f, CacheRatio: %f, GroupRatio: %f, UsePrice: %t, CacheCreationRatio: %f, CacheCreation5mRatio: %f, CacheCreation1hRatio: %f, QuotaToPreConsume: %d, ImageRatio: %f, AudioRatio: %f, AudioCompletionRatio: %f", p.ModelPrice, p.ModelRatio, p.CompletionRatio, p.CacheRatio, p.GroupRatioInfo.GroupRatio, p.UsePrice, p.CacheCreationRatio, p.CacheCreation5mRatio, p.CacheCreation1hRatio, p.QuotaToPreConsume, p.ImageRatio, p.AudioRatio, p.AudioCompletionRatio)
}
