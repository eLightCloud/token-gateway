package model

import (
	"net/mail"
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

// Contact details are management metadata. The account holder verifies email through the password setup link.
func (input *ResellerCreateInput) normalize() error {
	input.Username = strings.TrimSpace(input.Username)
	input.Email = NormalizeEmail(input.Email)
	if input.Username == "" || utf8.RuneCountInString(input.Username) > UserNameMaxLength || strings.ContainsRune(input.Username, 0) {
		return &ResellerCreateFieldError{Field: "username", Message: "Enter a valid username of up to 20 characters."}
	}
	if utf8.RuneCountInString(input.DisplayName) > 20 || strings.ContainsRune(input.DisplayName, 0) {
		return &ResellerCreateFieldError{Field: "display_name", Message: "Enter a valid display name of up to 20 characters."}
	}
	address, err := mail.ParseAddress(input.Email)
	if err != nil || address.Address != input.Email || len(input.Email) > 50 {
		return &ResellerCreateFieldError{Field: "email", Message: "Please enter a valid email address"}
	}
	input.Phone = strings.TrimSpace(input.Phone)
	input.Remark = strings.TrimSpace(input.Remark)
	if !resellerPhonePattern.MatchString(input.Phone) {
		return &ResellerCreateFieldError{Field: "phone", Message: "Enter 7–15 digits with an optional leading +"}
	}
	if utf8.RuneCountInString(input.Remark) > 255 || strings.ContainsRune(input.Remark, 0) {
		return &ResellerCreateFieldError{Field: "remark", Message: "Enter a valid remark of up to 255 characters."}
	}
	return nil
}
