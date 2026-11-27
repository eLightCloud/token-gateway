package model

import (
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/QuantumNous/new-api/types"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

var (
	ErrResellerCustomerDisabled        = errors.New("customer consumption is disabled")
	ErrResellerAgentDisabled           = errors.New("reseller consumption service is disabled")
	ErrResellerProcurementInsufficient = errors.New("reseller procurement quota is insufficient")
)

// LoadPersonalPricing reads only the requested subject/model, with no shared
// private-price cache. A single transaction freezes the parent/child revision.
func LoadPersonalPricing(userID int, modelName string) (*types.PersonalPricingSnapshot, error) {
	snapshots, err := loadPersonalPricings(userID, []string{modelName}, true)
	return snapshots[modelName], err
}

// LoadPersonalDisplayPricings reads a complete display slice in one snapshot.
// Admission and display share the same matrix validation and intersection.
func LoadPersonalDisplayPricings(userID int, modelNames []string) (map[string]*types.PersonalPricingSnapshot, error) {
	return loadPersonalPricings(userID, modelNames, false)
}

func loadPersonalPricings(userID int, modelNames []string, admission bool) (map[string]*types.PersonalPricingSnapshot, error) {
	snapshots := make(map[string]*types.PersonalPricingSnapshot, len(modelNames))
	keys := make([]string, 0, len(modelNames))
	models := make(map[string]string, len(modelNames))
	for _, name := range modelNames {
		key := fmt.Sprintf("%x", sha256.Sum256([]byte(name)))
		if existing, ok := models[key]; ok {
			if existing != name {
				return nil, ErrResellerDataCorrupt
			}
			continue
		}
		models[key] = name
		keys = append(keys, key)
	}
	options := &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true}
	if common.UsingMainDatabase(common.DatabaseTypeSQLite) {
		options = &sql.TxOptions{Isolation: sql.LevelSerializable}
	}
	err := DB.Transaction(func(tx *gorm.DB) error {
		var user User
		if err := tx.First(&user, userID).Error; err != nil {
			return err
		}
		if user.ResellerKind == "agent" {
			return ErrResellerForbidden
		}
		var head ResellerConfigHead
		if err := tx.First(&head, "user_id = ?", userID).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if user.ResellerKind == "" {
			for _, name := range modelNames {
				routingName := ratio_setting.RoutingMatchModelName(name)
				if routingName == name || routingName == "" {
					continue
				}
				key := fmt.Sprintf("%x", sha256.Sum256([]byte(routingName)))
				if existing, ok := models[key]; ok {
					if existing != routingName {
						return ErrResellerDataCorrupt
					}
					continue
				}
				models[key] = routingName
				keys = append(keys, key)
			}
		}
		var own []ResellerModelConfig
		if err := tx.Where("user_id = ? AND model_key IN ?", userID, keys).Find(&own).Error; err != nil {
			return err
		}
		for _, name := range modelNames {
			snapshots[name] = &types.PersonalPricingSnapshot{UserID: userID, Model: name, Revision: head.Revision, Channels: map[int]types.PersonalChannelPrice{}}
		}
		if user.ResellerKind == "" {
			if err := applyLegacyUserPricing(snapshots, own, models); err != nil {
				return err
			}
			return applyRealtimePricingTx(tx, user, snapshots)
		}
		if user.Status != common.UserStatusEnabled {
			return ErrResellerForbidden
		}
		identity, err := GetResellerIdentity(tx, userID)
		if err != nil {
			return err
		}
		if identity == nil || identity.Kind != "agent_customer" || identity.Activation != "active" {
			return ErrResellerForbidden
		}
		if identity.AgentDisabled {
			return ErrResellerCustomerDisabled
		}
		var parent User
		if err := tx.First(&parent, identity.AgentUserID).Error; err != nil {
			return err
		}
		var account ResellerAccount
		if err := tx.First(&account, "user_id = ?", identity.AgentUserID).Error; err != nil {
			return err
		}
		if parent.Status != common.UserStatusEnabled || !account.Enabled {
			return ErrResellerAgentDisabled
		}
		if admission && account.Balance <= account.Reserved {
			return ErrResellerProcurementInsufficient
		}
		head = ResellerConfigHead{}
		if err := tx.First(&head, "user_id = ?", identity.AgentUserID).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		for _, snapshot := range snapshots {
			snapshot.AgentUserID, snapshot.AgentRevision = identity.AgentUserID, head.Revision
		}
		var grants []ResellerModelConfig
		if err := tx.Where("user_id = ? AND model_key IN ?", identity.AgentUserID, keys).Find(&grants).Error; err != nil {
			return err
		}
		procurementScope := configuredResellerChannels(grants, true)
		parentRows := map[string]map[int]ResellerModelConfig{}
		for _, row := range grants {
			if models[row.ModelKey] != row.Model || row.Ratio < 1 || row.Ratio > maxResellerProcurementRatio {
				return ErrResellerDataCorrupt
			}
			if !row.Authorized {
				continue
			}
			if parentRows[row.Model] == nil {
				parentRows[row.Model] = map[int]ResellerModelConfig{}
			}
			parentRows[row.Model][row.ChannelID] = row
		}
		for _, row := range own {
			if models[row.ModelKey] != row.Model {
				return ErrResellerDataCorrupt
			}
			grant, ok := parentRows[row.Model][row.ChannelID]
			scope := procurementScope[row.Model]
			if !row.Authorized || !ok || (len(scope) > 0 && !scope[row.ChannelID]) {
				continue
			}
			if row.Ratio < 1 || row.Ratio > maxResellerConsumptionRatio {
				return ErrResellerDataCorrupt
			}
			snapshots[row.Model].Channels[row.ChannelID] = types.PersonalChannelPrice{CustomerRatio: row.Ratio, AgentRatio: grant.Ratio}
		}
		if err := applyRealtimePricingTx(tx, user, snapshots); err != nil {
			return err
		}
		for name, scope := range configuredResellerChannels(own, false) {
			snapshot := snapshots[name]
			if snapshot.Realtime != nil {
				continue
			}
			for id := range snapshot.Channels {
				if !scope[id] {
					delete(snapshot.Channels, id)
				}
			}
		}
		return nil
	}, options)
	return snapshots, err
}

func PersonalQuota(base int, ratio int64) (int, *common.QuotaClamp, error) {
	return resellerQuota(base, ratio, maxResellerConsumptionRatio)
}

func resellerQuota(base int, ratio, maxRatio int64) (int, *common.QuotaClamp, error) {
	if base < 0 || ratio < 1 || ratio > maxRatio {
		return 0, nil, ErrResellerInvalid
	}
	quota, clamp := common.QuotaFromDecimalChecked(decimal.NewFromInt(int64(base)).Mul(decimal.NewFromInt(ratio)).Div(decimal.NewFromInt(ResellerRatioScale)))
	return quota, clamp, nil
}
