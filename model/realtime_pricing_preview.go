package model

import (
	"database/sql"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/types"
	"gorm.io/gorm"
)

type RealtimePricingCandidate struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Status int    `json:"status"`
	Models string `json:"models"`
}

var ErrRealtimePricingMemberChanged = fmt.Errorf("%w: organization member changed", ErrResellerForbidden)

func RealtimePricingCandidates(tx *gorm.DB, operator int) ([]RealtimePricingCandidate, error) {
	var actor User
	if err := tx.First(&actor, operator).Error; err != nil {
		return nil, err
	}
	var channels []RealtimePricingCandidate
	if err := tx.Model(&Channel{}).Select("id", "name", "status", "models").Order("id").Find(&channels).Error; err != nil {
		return nil, err
	}
	if actor.ResellerKind != "agent" {
		return channels, nil
	}
	var grants []ResellerModelConfig
	if err := tx.Where("user_id = ? AND authorized = ?", operator, true).Find(&grants).Error; err != nil {
		return nil, err
	}
	byChannel := map[int][]string{}
	for _, g := range grants {
		byChannel[g.ChannelID] = append(byChannel[g.ChannelID], g.Model)
	}
	result := []RealtimePricingCandidate{}
	for _, ch := range channels {
		var models []string
		for _, name := range strings.Split(ch.Models, ",") {
			if slices.Contains(byChannel[ch.ID], name) {
				models = append(models, name)
			}
		}
		if len(models) > 0 {
			ch.Models = strings.Join(models, ",")
			result = append(result, ch)
		}
	}
	return result, nil
}

func resolveRealtimeChannel(personal, organization *RealtimePricingConfig, model string, channel int) types.PersonalChannelPrice {
	for _, entry := range []struct {
		config *RealtimePricingConfig
		source string
	}{{personal, "user"}, {organization, "organization"}} {
		for _, item := range entry.config.Items {
			if item.Model == model && slices.Contains(item.ChannelIDs, channel) {
				ratio, _ := ParseRealtimePricingRatio(item.Ratio)
				return types.PersonalChannelPrice{CustomerRatio: ratio, AgentRatio: ResellerRatioScale, Source: entry.source, Revision: entry.config.Revision}
			}
		}
	}
	return types.PersonalChannelPrice{CustomerRatio: ResellerRatioScale, AgentRatio: ResellerRatioScale, Source: "system"}
}

type RealtimePricingPreviewRow struct {
	Model     string `json:"model"`
	ChannelID int    `json:"channel_id"`
	Ratio     string `json:"ratio"`
	Source    string `json:"source"`
}
type RealtimePricingPreview struct {
	Rows                 []RealtimePricingPreviewRow `json:"rows"`
	UserRevision         int64                       `json:"user_revision"`
	OrganizationRevision int64                       `json:"organization_revision"`
	OrganizationID       int                         `json:"organization_id"`
	UserID               int                         `json:"user_id"`
	GeneratedAt          int64                       `json:"generated_at"`
}

func PreviewRealtimePricing(operator int, subject string, id, userID int, items []RealtimePricingItem) (*RealtimePricingPreview, error) {
	if len(items) > 10000 {
		return nil, ErrResellerInvalid
	}
	seen := map[string]bool{}
	for _, item := range items {
		if _, err := ParseRealtimePricingRatio(item.Ratio); err != nil || item.Model == "" || seen[item.Model] || len(item.ChannelIDs) == 0 {
			return nil, ErrResellerInvalid
		}
		seen[item.Model] = true
	}
	options := &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true}
	if common.UsingMainDatabase(common.DatabaseTypeSQLite) {
		options = &sql.TxOptions{Isolation: sql.LevelSerializable}
	}
	result := &RealtimePricingPreview{Rows: []RealtimePricingPreviewRow{}, GeneratedAt: common.GetTimestamp()}
	err := DB.Transaction(func(tx *gorm.DB) error {
		if err := AuthorizeRealtimePricing(tx, operator, subject, id); err != nil {
			return err
		}
		personal := &RealtimePricingConfig{Items: []RealtimePricingItem{}}
		organization := &RealtimePricingConfig{Items: []RealtimePricingItem{}}
		var err error
		if subject == "user" {
			userID = id
		} else {
			result.OrganizationID = id
		}
		result.UserID = userID
		if userID > 0 {
			personal, err = ReadRealtimePricing(tx, "user", userID)
			if err != nil {
				return err
			}
			var members []OrganizationMember
			if err := tx.Where("user_id = ? AND left_at = 0", userID).Limit(2).Find(&members).Error; err != nil {
				return err
			}
			if len(members) > 1 {
				return ErrResellerDataCorrupt
			}
			if subject == "organization" && (len(members) != 1 || members[0].OrganizationId != id) {
				return ErrRealtimePricingMemberChanged
			}
			if len(members) == 1 {
				result.OrganizationID = members[0].OrganizationId
			}
		}
		if result.OrganizationID > 0 {
			organization, err = ReadRealtimePricing(tx, "organization", result.OrganizationID)
			if err != nil {
				return err
			}
		}
		result.UserRevision = personal.Revision
		result.OrganizationRevision = organization.Revision
		if subject == "user" {
			personal.Items = items
		} else {
			organization.Items = items
		}
		candidates, err := RealtimePricingCandidates(tx, operator)
		if err != nil {
			return err
		}
		var customer User
		if userID > 0 {
			if err := tx.First(&customer, userID).Error; err != nil {
				return err
			}
		}
		allowed := map[string]bool{}
		if customer.ResellerKind == "agent_customer" {
			var grants []ResellerModelConfig
			if err := tx.Where("user_id = ? AND authorized = ?", userID, true).Find(&grants).Error; err != nil {
				return err
			}
			identity, err := GetResellerIdentity(tx, userID)
			if err != nil {
				return err
			}
			if identity == nil {
				return ErrResellerDataCorrupt
			}
			var parent []ResellerModelConfig
			if err := tx.Where("user_id = ? AND authorized = ?", identity.AgentUserID, true).Find(&parent).Error; err != nil {
				return err
			}
			for _, row := range grants {
				for _, p := range parent {
					if row.ChannelID == p.ChannelID && row.Model == p.Model {
						allowed[row.Model+":"+strconv.Itoa(row.ChannelID)] = true
					}
				}
			}
		}
		for _, ch := range candidates {
			for _, name := range strings.Split(ch.Models, ",") {
				if name == "" {
					continue
				}
				if customer.ResellerKind == "agent_customer" && !allowed[name+":"+strconv.Itoa(ch.ID)] {
					continue
				}
				price := resolveRealtimeChannel(personal, organization, name, ch.ID)
				result.Rows = append(result.Rows, RealtimePricingPreviewRow{Model: name, ChannelID: ch.ID, Ratio: formatPersonalRatio(price.CustomerRatio), Source: price.Source})
			}
		}
		return nil
	}, options)
	return result, err
}

// Legacy rows remain authorization data after the consumption cutover.
func validateRealtimeAuthorizationWrite(tx *gorm.DB, userID int, input ResellerConfigInput) error {
	var previous []ResellerModelConfig
	if err := tx.Where("user_id = ?", userID).Find(&previous).Error; err != nil {
		return err
	}
	for _, item := range input.Items {
		ratio := item.Ratio
		if ratio == "" {
			ratio = "1"
		}
		parsed, err := ParseResellerRatio(ratio)
		if err != nil {
			return err
		}
		old := int64(ResellerRatioScale)
		for _, row := range previous {
			if row.Model == item.Model && row.ChannelID == item.ChannelID {
				old = row.Ratio
				break
			}
		}
		if parsed != old {
			return fmt.Errorf("%w: consumption ratio moved to realtime pricing", ErrResellerConflict)
		}
	}
	return nil
}
