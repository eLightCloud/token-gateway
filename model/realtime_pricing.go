package model

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/types"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const RealtimePricingVersion = 2

// A configuration head survives deletion of its last rule.
type RealtimePricingHead struct {
	Subject   string `gorm:"size:16;primaryKey" json:"subject"`
	SubjectID int    `gorm:"primaryKey;autoIncrement:false" json:"subject_id"`
	Revision  int64  `json:"revision"`
}

type RealtimePricingRule struct {
	ID        int64  `gorm:"primaryKey"`
	Subject   string `gorm:"size:16;uniqueIndex:realtime_rule_key"`
	SubjectID int    `gorm:"uniqueIndex:realtime_rule_key"`
	ModelKey  string `gorm:"size:64;uniqueIndex:realtime_rule_key"`
	Model     string `gorm:"type:text;not null"`
	Ratio     int64  `gorm:"not null"`
	Channels  string `gorm:"type:text;not null"`
}

type RealtimePricingHistory struct {
	ID         int64  `gorm:"primaryKey" json:"id"`
	Subject    string `gorm:"size:16;uniqueIndex:realtime_history_key" json:"-"`
	SubjectID  int    `gorm:"uniqueIndex:realtime_history_key" json:"-"`
	Revision   int64  `gorm:"uniqueIndex:realtime_history_key" json:"revision"`
	OperatorID int    `json:"operator_id"`
	Snapshot   string `gorm:"type:text;not null" json:"-"`
	CreatedAt  int64  `json:"created_at"`
}

// The singleton is a database fence shared by every reader/writer instance.
// No process-local activation flag can split pricing across nodes.
type RealtimePricingState struct {
	ID    int    `gorm:"primaryKey;autoIncrement:false" json:"-"`
	Phase string `gorm:"size:16;not null" json:"phase"`
}

type RealtimePricingItem struct {
	Model      string `json:"model"`
	Ratio      string `json:"ratio"`
	ChannelIDs []int  `json:"channel_ids"`
}
type RealtimePricingConfig struct {
	Revision int64                 `json:"revision"`
	Items    []RealtimePricingItem `json:"items"`
}
type RealtimePricingInput struct {
	ExpectedRevision int64                 `json:"expected_revision"`
	Items            []RealtimePricingItem `json:"items"`
}

var realtimeRatioPattern = regexp.MustCompile(`^(?:[0-4](?:\.[0-9]{1,2})?|5(?:\.0{1,2})?)$`)

func ParseRealtimePricingRatio(value string) (int64, error) {
	if !realtimeRatioPattern.MatchString(value) {
		return 0, ErrResellerInvalid
	}
	d, err := decimal.NewFromString(value)
	if err != nil || !d.IsPositive() {
		return 0, ErrResellerInvalid
	}
	return d.Mul(decimal.NewFromInt(ResellerRatioScale)).IntPart(), nil
}

func RealtimeCustomerQuota(base int, ratio int64, version int) (int, *common.QuotaClamp, error) {
	if version == 0 {
		return PersonalQuota(base, ratio)
	}
	if version != RealtimePricingVersion || base < 0 || ratio < 10000 || ratio > 5000000 || ratio%10000 != 0 {
		return 0, nil, ErrResellerInvalid
	}
	quota, clamp := common.QuotaFromDecimalChecked(decimal.NewFromInt(int64(base)).Mul(decimal.NewFromInt(ratio)).Div(decimal.NewFromInt(ResellerRatioScale)))
	return quota, clamp, nil
}

func RealtimePricingPhase(tx *gorm.DB) (string, error) {
	var state RealtimePricingState
	err := tx.First(&state, "id = ?", 1).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "legacy", nil
	}
	if err != nil {
		return "", err
	}
	if !slices.Contains([]string{"legacy", "frozen", "active"}, state.Phase) {
		return "", ErrResellerDataCorrupt
	}
	return state.Phase, nil
}

func ReadRealtimePricing(tx *gorm.DB, subject string, id int, modelNames ...string) (*RealtimePricingConfig, error) {
	if (subject != "user" && subject != "organization") || id <= 0 {
		return nil, ErrResellerInvalid
	}
	var head RealtimePricingHead
	err := tx.First(&head, "subject = ? AND subject_id = ?", subject, id).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	var rows []RealtimePricingRule
	query := tx.Where("subject = ? AND subject_id = ?", subject, id)
	if len(modelNames) > 0 {
		keys := make([]string, 0, len(modelNames))
		for _, name := range modelNames {
			keys = append(keys, fmt.Sprintf("%x", sha256.Sum256([]byte(name))))
		}
		query = query.Where("model_key IN ?", keys)
	}
	if err := query.Order("id").Find(&rows).Error; err != nil {
		return nil, err
	}
	if len(rows) > 0 && head.Revision <= 0 {
		return nil, ErrResellerDataCorrupt
	}
	result := &RealtimePricingConfig{Revision: head.Revision, Items: []RealtimePricingItem{}}
	for _, row := range rows {
		var ids []int
		if row.ModelKey != fmt.Sprintf("%x", sha256.Sum256([]byte(row.Model))) || common.UnmarshalJsonStr(row.Channels, &ids) != nil || len(ids) == 0 {
			return nil, ErrResellerDataCorrupt
		}
		if _, _, err := RealtimeCustomerQuota(0, row.Ratio, RealtimePricingVersion); err != nil {
			return nil, ErrResellerDataCorrupt
		}
		seen := map[int]bool{}
		for _, channelID := range ids {
			if channelID <= 0 || seen[channelID] {
				return nil, ErrResellerDataCorrupt
			}
			seen[channelID] = true
		}
		result.Items = append(result.Items, RealtimePricingItem{Model: row.Model, Ratio: formatPersonalRatio(row.Ratio), ChannelIDs: ids})
	}
	return result, nil
}

func SaveRealtimePricing(operator int, subject string, id int, input RealtimePricingInput) (*RealtimePricingConfig, error) {
	var result *RealtimePricingConfig
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := AuthorizeRealtimePricing(tx, operator, subject, id); err != nil {
			return err
		}
		if input.ExpectedRevision < 0 || len(input.Items) > 10000 {
			return ErrResellerInvalid
		}
		phase, err := RealtimePricingPhase(tx)
		if err != nil {
			return err
		}
		if phase != "active" {
			return fmt.Errorf("%w: realtime pricing is not active", ErrResellerConflict)
		}
		previous, err := ReadRealtimePricing(tx, subject, id)
		if err != nil {
			return err
		}
		if previous.Revision != input.ExpectedRevision {
			return &ResellerRevisionConflictError{Revision: previous.Revision}
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&RealtimePricingHead{Subject: subject, SubjectID: id}).Error; err != nil {
			return err
		}
		updated := tx.Model(&RealtimePricingHead{}).Where("subject = ? AND subject_id = ? AND revision = ?", subject, id, input.ExpectedRevision).Update("revision", gorm.Expr("revision + 1"))
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			var current RealtimePricingHead
			if err := lockForUpdate(tx).First(&current, "subject = ? AND subject_id = ?", subject, id).Error; err != nil {
				return err
			}
			return &ResellerRevisionConflictError{Revision: current.Revision}
		}
		prior := map[string]map[int]bool{}
		for _, item := range previous.Items {
			prior[item.Model] = map[int]bool{}
			for _, channel := range item.ChannelIDs {
				prior[item.Model][channel] = true
			}
		}
		candidates, err := RealtimePricingCandidates(tx, operator)
		if err != nil {
			return err
		}
		allowed := map[string]map[int]bool{}
		for _, ch := range candidates {
			for name := range strings.SplitSeq(ch.Models, ",") {
				if allowed[name] == nil {
					allowed[name] = map[int]bool{}
				}
				allowed[name][ch.ID] = true
			}
		}
		seen := map[string]bool{}
		rows := make([]RealtimePricingRule, 0, len(input.Items))
		items := make([]RealtimePricingItem, 0, len(input.Items))
		for _, item := range input.Items {
			ratio, err := ParseRealtimePricingRatio(item.Ratio)
			if err != nil || item.Model == "" || len(item.Model) > 512 || len(item.ChannelIDs) == 0 || len(item.ChannelIDs) > 10000 || seen[item.Model] {
				return ErrResellerInvalid
			}
			seen[item.Model] = true
			ids := slices.Clone(item.ChannelIDs)
			slices.Sort(ids)
			for i, channelID := range ids {
				if channelID <= 0 || i > 0 && ids[i-1] == channelID {
					return ErrResellerInvalid
				}
				if !allowed[item.Model][channelID] && !prior[item.Model][channelID] {
					return ErrResellerForbidden
				}
				var channel Channel
				err := tx.Select("id", "models").First(&channel, channelID).Error
				if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
					return err
				}
				if (err != nil || !slices.Contains(strings.Split(channel.Models, ","), item.Model)) && !prior[item.Model][channelID] {
					return ErrResellerInvalid
				}
			}
			encoded, err := common.Marshal(ids)
			if err != nil {
				return err
			}
			items = append(items, RealtimePricingItem{Model: item.Model, Ratio: formatPersonalRatio(ratio), ChannelIDs: ids})
			rows = append(rows, RealtimePricingRule{Subject: subject, SubjectID: id, ModelKey: fmt.Sprintf("%x", sha256.Sum256([]byte(item.Model))), Model: item.Model, Ratio: ratio, Channels: string(encoded)})
		}
		if err := tx.Where("subject = ? AND subject_id = ?", subject, id).Delete(&RealtimePricingRule{}).Error; err != nil {
			return err
		}
		if len(rows) > 0 {
			if err := tx.CreateInBatches(&rows, 100).Error; err != nil {
				return err
			}
		}
		encoded, err := common.Marshal(items)
		if err != nil {
			return err
		}
		if len(encoded) > 65535 {
			return ErrResellerInvalid
		}
		result = &RealtimePricingConfig{Revision: input.ExpectedRevision + 1, Items: items}
		return tx.Create(&RealtimePricingHistory{Subject: subject, SubjectID: id, Revision: result.Revision, OperatorID: operator, Snapshot: string(encoded), CreatedAt: common.GetTimestamp()}).Error
	})
	return result, err
}

// applyRealtimePricingTx overlays consumption only. Parent grants and procurement
// prices have already been validated by loadPersonalPricings in this same tx.
func applyRealtimePricingTx(tx *gorm.DB, user User, snapshots map[string]*types.PersonalPricingSnapshot) error {
	phase, err := RealtimePricingPhase(tx)
	if err != nil || phase != "active" {
		return err
	}
	modelNames := make([]string, 0, len(snapshots))
	for name := range snapshots {
		modelNames = append(modelNames, name)
	}
	if len(modelNames) == 0 {
		return nil
	}
	personal, err := ReadRealtimePricing(tx, "user", user.Id, modelNames...)
	if err != nil {
		return err
	}
	var members []OrganizationMember
	if err := tx.Where("user_id = ? AND left_at = 0", user.Id).Limit(2).Find(&members).Error; err != nil {
		return err
	}
	if len(members) > 1 {
		return ErrResellerDataCorrupt
	}
	organizationID := 0
	organization := &RealtimePricingConfig{Items: []RealtimePricingItem{}}
	var oldDiscount *types.OrganizationDiscountSnapshot
	if len(members) == 1 {
		organizationID = members[0].OrganizationId
		var org Organization
		if err := tx.First(&org, organizationID).Error; err != nil {
			return err
		}
		organization, err = ReadRealtimePricing(tx, "organization", organizationID, modelNames...)
		if err != nil {
			return err
		}
		if org.CurrentDiscountSnapshotId > 0 {
			var old OrganizationDiscountSnapshot
			if err := tx.First(&old, "id = ? AND organization_id = ?", org.CurrentDiscountSnapshotId, org.Id).Error; err != nil {
				return err
			}
			discounts, err := UnmarshalOrganizationChannelDiscounts(old.ChannelDiscounts)
			if err != nil {
				return err
			}
			oldDiscount = &types.OrganizationDiscountSnapshot{SnapshotID: old.Id, ChannelDiscounts: map[int]float64{}}
			for id, ratio := range discounts {
				oldDiscount.ChannelDiscounts[id] = OrganizationDiscountRatioFloatFromScaled(ratio)
			}
		}
	}
	for name, snapshot := range snapshots {
		if snapshot == nil {
			snapshot = &types.PersonalPricingSnapshot{UserID: user.Id, Model: name, Channels: map[int]types.PersonalChannelPrice{}}
		}
		oldChannels := snapshot.Channels
		channels := map[int]types.PersonalChannelPrice{}
		for _, config := range []struct {
			config *RealtimePricingConfig
			source string
		}{{organization, "organization"}, {personal, "user"}} {
			for _, item := range config.config.Items {
				if item.Model != name {
					continue
				}
				if _, err := ParseRealtimePricingRatio(item.Ratio); err != nil {
					return err
				}
				for _, id := range item.ChannelIDs {
					channels[id] = resolveRealtimeChannel(personal, organization, name, id)
				}
			}
		}
		if snapshot.AgentUserID > 0 {
			for id, old := range oldChannels {
				price, ok := channels[id]
				if !ok {
					price = types.PersonalChannelPrice{CustomerRatio: ResellerRatioScale, Source: "system"}
				}
				price.AgentRatio = old.AgentRatio
				oldChannels[id] = price
			}
			channels = oldChannels
		}
		snapshot.Channels = channels
		snapshot.Realtime = &types.RealtimePricingContext{Version: RealtimePricingVersion, OrganizationID: organizationID, UserRevision: personal.Revision, OrganizationRevision: organization.Revision, LegacyDiscount: oldDiscount}
		snapshots[name] = snapshot
	}
	return nil
}

// RealtimeMigrationConflict describes data requiring an explicit operator choice.
type RealtimeMigrationConflict struct {
	ChannelID int    `json:"channel_id"`
	Ratio     string `json:"ratio"`
	UserID    int    `json:"user_id"`
	Model     string `json:"model"`
	Reason    string `json:"reason"`
}

func MigrateRealtimePricing(operator int, action string) ([]RealtimeMigrationConflict, error) {
	conflicts := []RealtimeMigrationConflict{}
	err := DB.Transaction(func(tx *gorm.DB) error {
		var actor User
		if err := tx.First(&actor, operator).Error; err != nil {
			return err
		}
		if actor.Role != common.RoleRootUser || actor.ResellerKind != "" || actor.Status != common.UserStatusEnabled {
			return ErrResellerForbidden
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&RealtimePricingState{ID: 1, Phase: "legacy"}).Error; err != nil {
			return err
		}
		var state RealtimePricingState
		if err := lockForUpdate(tx).First(&state, 1).Error; err != nil {
			return err
		}
		if action == "freeze" && state.Phase == "legacy" {
			return tx.Model(&state).Update("phase", "frozen").Error
		}
		if action == "resume" && state.Phase == "frozen" {
			return tx.Model(&state).Update("phase", "legacy").Error
		}
		if action != "activate" || state.Phase != "frozen" {
			return ErrResellerConflict
		}
		var old []ResellerModelConfig
		if err := tx.Where("user_id IN (?)", tx.Model(&User{}).Select("id").Where("reseller_kind <> ? OR reseller_kind IS NULL", "agent")).Order("id").Find(&old).Error; err != nil {
			return err
		}
		groups := map[string]RealtimePricingItem{}
		users := map[string]int{}
		for _, row := range old {
			key := strconv.Itoa(row.UserID) + ":" + row.Model
			item, exists := groups[key]
			if row.ModelKey != fmt.Sprintf("%x", sha256.Sum256([]byte(row.Model))) {
				return ErrResellerDataCorrupt
			}
			if _, _, err := RealtimeCustomerQuota(0, row.Ratio, RealtimePricingVersion); err != nil {
				conflicts = append(conflicts, RealtimeMigrationConflict{UserID: row.UserID, Model: row.Model, ChannelID: row.ChannelID, Ratio: formatPersonalRatio(row.Ratio), Reason: "ratio must be in (0, 5] with at most two decimal places"})
			}
			if exists && item.Ratio != formatPersonalRatio(row.Ratio) {
				conflicts = append(conflicts, RealtimeMigrationConflict{UserID: row.UserID, Model: row.Model, ChannelID: row.ChannelID, Ratio: formatPersonalRatio(row.Ratio), Reason: "multiple ratios for one model"})
			}
			item.Model = row.Model
			item.Ratio = formatPersonalRatio(row.Ratio)
			item.ChannelIDs = append(item.ChannelIDs, row.ChannelID)
			groups[key] = item
			users[key] = row.UserID
		}
		if len(conflicts) > 0 {
			return nil
		}
		byUser := map[int][]RealtimePricingItem{}
		for key, item := range groups {
			byUser[users[key]] = append(byUser[users[key]], item)
		}
		for id, items := range byUser {
			slices.SortFunc(items, func(a, b RealtimePricingItem) int { return strings.Compare(a.Model, b.Model) })
			for _, item := range items {
				encoded, err := common.Marshal(item.ChannelIDs)
				if err != nil {
					return err
				}
				ratio, _ := ParseRealtimePricingRatio(item.Ratio)
				if err := tx.Create(&RealtimePricingRule{Subject: "user", SubjectID: id, ModelKey: fmt.Sprintf("%x", sha256.Sum256([]byte(item.Model))), Model: item.Model, Ratio: ratio, Channels: string(encoded)}).Error; err != nil {
					return err
				}
			}
			if err := tx.Create(&RealtimePricingHead{Subject: "user", SubjectID: id, Revision: 1}).Error; err != nil {
				return err
			}
			encoded, err := common.Marshal(items)
			if err != nil {
				return err
			}
			if err := tx.Create(&RealtimePricingHistory{Subject: "user", SubjectID: id, Revision: 1, OperatorID: operator, Snapshot: string(encoded), CreatedAt: common.GetTimestamp()}).Error; err != nil {
				return err
			}
		}
		return tx.Model(&state).Update("phase", "active").Error
	})
	return conflicts, err
}

// guardRealtimeLegacyWriteTx shares the migration row lock with activation.
func guardRealtimeLegacyWriteTx(tx *gorm.DB, userID int, input ResellerConfigInput) error {
	var user User
	if err := tx.Select("id", "reseller_kind").First(&user, userID).Error; err != nil {
		return err
	}
	if user.ResellerKind == "agent" {
		return nil
	}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&RealtimePricingState{ID: 1, Phase: "legacy"}).Error; err != nil {
		return err
	}
	var state RealtimePricingState
	err := lockForUpdate(tx).First(&state, 1).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if state.Phase == "active" {
		if user.ResellerKind != "agent_customer" {
			return fmt.Errorf("%w: consumption ratio moved to realtime pricing", ErrResellerConflict)
		}
		return validateRealtimeAuthorizationWrite(tx, userID, input)
	}
	if state.Phase == "frozen" {
		return ErrResellerConflict
	}
	return nil
}

func AuthorizeRealtimePricing(tx *gorm.DB, operator int, subject string, id int) error {
	var actor User
	if err := tx.First(&actor, operator).Error; err != nil {
		return err
	}
	if actor.Status != common.UserStatusEnabled {
		return ErrResellerForbidden
	}
	if subject == "organization" {
		if actor.Role != common.RoleRootUser || actor.ResellerKind != "" {
			return ErrResellerForbidden
		}
		var org Organization
		return tx.First(&org, id).Error
	}
	if subject != "user" {
		return ErrResellerInvalid
	}
	var target User
	if err := tx.First(&target, id).Error; err != nil {
		return err
	}
	if target.ResellerKind == "agent" {
		return ErrResellerForbidden
	}
	return ResellerManageTarget(tx, operator, id, actor.Role >= common.RoleAdminUser && actor.ResellerKind == "")
}
