package model

import (
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

type ResellerOwnAccount struct {
	User      ResellerUserView     `json:"user"`
	Discounts []ResellerConfigItem `json:"discounts"`
}

type ResellerOwnProfileInput struct {
	DisplayName *string `json:"display_name"`
	Phone       *string `json:"phone"`
}

func readResellerOwnProfile(tx *gorm.DB, userID int) (*ResellerUserView, error) {
	if userID <= 0 {
		return nil, ErrResellerForbidden
	}
	var user User
	if err := tx.Select("id", "username", "display_name", "email", "reseller_kind", "status").First(&user, userID).Error; err != nil {
		return nil, err
	}
	if user.ResellerKind != "agent" || user.Status != common.UserStatusEnabled {
		return nil, ErrResellerForbidden
	}
	identity, err := GetResellerIdentity(tx, userID)
	if err != nil {
		return nil, err
	}
	if identity == nil || identity.Kind != "agent" || identity.Activation != "active" {
		return nil, ErrResellerForbidden
	}
	var account ResellerAccount
	if err := tx.First(&account, "user_id = ?", userID).Error; err != nil {
		return nil, err
	}
	if !account.Enabled {
		return nil, ErrResellerForbidden
	}
	return &ResellerUserView{
		UserID: userID, Username: user.Username, DisplayName: user.DisplayName,
		Email: user.Email, Phone: identity.Phone, Activation: identity.Activation, Account: &account,
	}, nil
}

func ReadResellerOwnAccount(userID int) (*ResellerOwnAccount, error) {
	profile, err := readResellerOwnProfile(DB, userID)
	if err != nil {
		return nil, err
	}
	config, err := ReadResellerConfig(DB, userID)
	if err != nil {
		return nil, err
	}
	result := &ResellerOwnAccount{User: *profile, Discounts: []ResellerConfigItem{}}
	discounts := make([]ResellerConfigItem, 0, len(config.Items))
	channelIDs := make([]int, 0, len(config.Items))
	for _, item := range config.Items {
		// Config ratios are canonical strings, so only "0.*" represents a discount.
		if item.Authorized && strings.HasPrefix(item.Ratio, "0.") {
			discounts = append(discounts, item)
			channelIDs = append(channelIDs, item.ChannelID)
		}
	}
	if len(discounts) == 0 {
		return result, nil
	}
	var channels []Channel
	if err := DB.Select("id", "models").Where("id IN ?", channelIDs).Find(&channels).Error; err != nil {
		return nil, err
	}
	modelsByChannel := make(map[int]map[string]bool, len(channels))
	for _, channel := range channels {
		modelsByChannel[channel.Id] = make(map[string]bool)
		for name := range strings.SplitSeq(channel.Models, ",") {
			modelsByChannel[channel.Id][name] = true
		}
	}
	for _, item := range discounts {
		if modelsByChannel[item.ChannelID][item.Model] {
			result.Discounts = append(result.Discounts, item)
		}
	}
	return result, nil
}

func UpdateResellerOwnProfile(userID int, input ResellerOwnProfileInput) error {
	if input.DisplayName == nil && input.Phone == nil {
		return ErrResellerInvalid
	}
	var name, phone string
	if input.DisplayName != nil {
		name = strings.TrimSpace(*input.DisplayName)
		if utf8.RuneCountInString(name) > 20 || strings.ContainsRune(name, 0) {
			return ErrResellerInvalid
		}
	}
	if input.Phone != nil {
		phone = strings.TrimSpace(*input.Phone)
		if phone != "" && !resellerPhonePattern.MatchString(phone) {
			return ErrResellerInvalid
		}
	}
	return resellerWriteError(DB.Transaction(func(tx *gorm.DB) error {
		profile, err := readResellerOwnProfile(tx, userID)
		if err != nil {
			return err
		}
		if input.DisplayName != nil && name != profile.DisplayName {
			if err := tx.Model(&User{}).Where("id = ?", userID).Update("display_name", name).Error; err != nil {
				return err
			}
		}
		if input.Phone != nil && phone != profile.Phone {
			return tx.Model(&ResellerIdentity{}).Where("user_id = ?", userID).Update("phone", phone).Error
		}
		return nil
	}))
}
