package model

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

type ResellerCreateInput struct {
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	Email       string `json:"email"`
	Phone       string `json:"phone"`
	Remark      string `json:"remark"`
}

var resellerPhonePattern = regexp.MustCompile(`^\+?[0-9]{7,15}$`)

// Contact details are management metadata. Account activation uses email and MFA.
func (input *ResellerCreateInput) normalizeContact(kind string) error {
	if kind != "agent" {
		input.Phone, input.Remark = "", ""
		return nil
	}
	input.Phone = strings.TrimSpace(input.Phone)
	input.Remark = strings.TrimSpace(input.Remark)
	if !resellerPhonePattern.MatchString(input.Phone) || utf8.RuneCountInString(input.Remark) > 255 || strings.ContainsRune(input.Remark, 0) {
		return ErrResellerInvalid
	}
	return nil
}
