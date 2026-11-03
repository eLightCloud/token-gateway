package model

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const ResellerRatioScale int64 = 1_000_000

var resellerRatioPattern = regexp.MustCompile(`^(0|1|2)(\.[0-9]{1,6})?$`)

func ParseResellerRatio(value string) (int64, error) {
	if !resellerRatioPattern.MatchString(value) {
		return 0, ErrResellerInvalid
	}
	whole, fraction, _ := strings.Cut(value, ".")
	integer, _ := strconv.ParseInt(whole, 10, 64)
	fraction += strings.Repeat("0", 6-len(fraction))
	part, _ := strconv.ParseInt(fraction, 10, 64)
	ratio := integer*ResellerRatioScale + part
	if ratio < 1 || ratio > 2*ResellerRatioScale {
		return 0, ErrResellerInvalid
	}
	return ratio, nil
}

type ResellerConfigHead struct {
	UserID   int   `gorm:"primaryKey;autoIncrement:false" json:"user_id"`
	Revision int64 `json:"revision"`
}
type ResellerConfigItem struct {
	Model      string `json:"model"`
	ChannelID  int    `json:"channel_id"`
	Authorized bool   `json:"authorized"`
	Ratio      string `json:"ratio"`
}
type ResellerConfigInput struct {
	ExpectedRevision int64                `json:"expected_revision"`
	Items            []ResellerConfigItem `json:"items"`
}
type ResellerConfigView struct {
	Revision int64                `json:"revision"`
	Items    []ResellerConfigItem `json:"items"`
}

// ResellerManageTarget scopes every dedicated resource before reading its data.
func ResellerManageTarget(tx *gorm.DB, operatorID, targetID int, admin bool) error {
	var operator User
	if err := tx.First(&operator, operatorID).Error; err != nil {
		return err
	}
	if operator.Status != common.UserStatusEnabled {
		return ErrResellerForbidden
	}
	identity, err := GetResellerIdentity(tx, targetID)
	if err != nil {
		return err
	}
	if admin {
		if operator.ResellerKind != "" || operator.Role < common.RoleAdminUser {
			return ErrResellerForbidden
		}
		if identity != nil && identity.Kind == "agent_customer" {
			return ErrResellerForbidden
		}
		var target User
		return tx.First(&target, targetID).Error
	}
	parent, err := GetResellerIdentity(tx, operatorID)
	if err != nil {
		return err
	}
	if parent == nil || parent.Kind != "agent" || parent.Activation != "active" {
		return ErrResellerForbidden
	}
	var account ResellerAccount
	if err := tx.First(&account, "user_id = ?", operatorID).Error; err != nil {
		return err
	}
	if !account.Enabled {
		return ErrResellerForbidden
	}
	if identity == nil || identity.Kind != "agent_customer" || identity.AgentUserID != operatorID {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func ReadResellerConfig(tx *gorm.DB, userID int) (*ResellerConfigView, error) {
	var head ResellerConfigHead
	err := tx.First(&head, "user_id = ?", userID).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}
	var rows []ResellerModelConfig
	if err := tx.Where("user_id = ?", userID).Order("id").Find(&rows).Error; err != nil {
		return nil, err
	}
	view := &ResellerConfigView{Revision: head.Revision, Items: make([]ResellerConfigItem, 0, len(rows))}
	for _, row := range rows {
		if row.ModelKey != fmt.Sprintf("%x", sha256.Sum256([]byte(row.Model))) || row.Ratio < 1 || row.Ratio > 2*ResellerRatioScale {
			return nil, ErrResellerInvalid
		}
		view.Items = append(view.Items, ResellerConfigItem{Model: row.Model, ChannelID: row.ChannelID, Authorized: row.Authorized, Ratio: fmt.Sprintf("%d.%06d", row.Ratio/ResellerRatioScale, row.Ratio%ResellerRatioScale)})
	}
	return view, nil
}

// SaveResellerConfig replaces one subject's matrix under an optimistic revision.
// Revoked parent grants remain stored on the child and may be restored later.
func SaveResellerConfig(operatorID, userID int, admin bool, input ResellerConfigInput) (*ResellerConfigView, error) {
	if input.ExpectedRevision < 0 || len(input.Items) > 10000 {
		return nil, ErrResellerInvalid
	}
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := ResellerManageTarget(tx, operatorID, userID, admin); err != nil {
			return err
		}
		if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&ResellerConfigHead{UserID: userID}).Error; err != nil {
			return err
		}
		result := tx.Model(&ResellerConfigHead{}).Where("user_id = ? AND revision = ?", userID, input.ExpectedRevision).Update("revision", gorm.Expr("revision + 1"))
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrResellerConflict
		}
		var previous []ResellerModelConfig
		if err := tx.Where("user_id = ?", userID).Find(&previous).Error; err != nil {
			return err
		}
		parent := map[string]bool{}
		old := map[string]ResellerModelConfig{}
		for _, item := range previous {
			old[fmt.Sprintf("%s:%d", item.Model, item.ChannelID)] = item
		}
		if !admin {
			var rows []ResellerModelConfig
			if err := tx.Where("user_id = ? AND authorized = ?", operatorID, true).Find(&rows).Error; err != nil {
				return err
			}
			for _, row := range rows {
				parent[fmt.Sprintf("%s:%d", row.Model, row.ChannelID)] = true
			}
		}
		rows := make([]ResellerModelConfig, 0, len(input.Items))
		seen := map[string]bool{}
		for _, item := range input.Items {
			ratio, err := ParseResellerRatio(item.Ratio)
			if err != nil || item.Model == "" || len(item.Model) > 512 || item.ChannelID <= 0 {
				return ErrResellerInvalid
			}
			key := fmt.Sprintf("%s:%d", item.Model, item.ChannelID)
			if seen[key] {
				return ErrResellerInvalid
			}
			seen[key] = true
			prior, existed := old[key]
			if !admin && item.Authorized && !parent[key] && (!existed || !prior.Authorized || prior.Ratio != ratio) {
				return ErrResellerForbidden
			}
			var channel Channel
			if err := tx.First(&channel, item.ChannelID).Error; err != nil {
				if !existed || item.Authorized != prior.Authorized || ratio != prior.Ratio {
					return ErrResellerInvalid
				}
			} else if !existed && !strings.Contains(","+channel.Models+",", ","+item.Model+",") {
				return ErrResellerInvalid
			}
			rows = append(rows, ResellerModelConfig{UserID: userID, ModelKey: fmt.Sprintf("%x", sha256.Sum256([]byte(item.Model))), Model: item.Model, ChannelID: item.ChannelID, Authorized: item.Authorized, Ratio: ratio, Revision: input.ExpectedRevision + 1})
		}
		if err := tx.Where("user_id = ?", userID).Delete(&ResellerModelConfig{}).Error; err != nil {
			return err
		}
		if len(rows) > 0 {
			if err := tx.Create(&rows).Error; err != nil {
				return err
			}
		}
		snapshot, err := common.Marshal(input.Items)
		if err != nil {
			return err
		}
		return tx.Create(&ResellerConfigRevision{UserID: userID, Revision: input.ExpectedRevision + 1, OperatorID: operatorID, Snapshot: string(snapshot), CreatedAt: common.GetTimestamp()}).Error
	})
	if err != nil {
		return nil, resellerWriteError(err)
	}
	return ReadResellerConfig(DB, userID)
}
