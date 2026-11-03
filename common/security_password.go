package common

import (
	_ "embed"
	"errors"
	"strings"
	"sync"
)

// Derived from SecLists xato-net-10-million-passwords-100000.txt, retaining the
// first 3000 distinct passwords matching our 8..128 character policy.
// Source revision and MIT attribution are in security_passwords.LICENSE.
//
//go:embed security_passwords.txt
var securityCommonPasswords string
var securityCommonPasswordSet = sync.OnceValue(func() map[string]struct{} {
	values := make(map[string]struct{}, 3000)
	for value := range strings.SplitSeq(securityCommonPasswords, "\n") {
		if value != "" {
			values[value] = struct{}{}
		}
	}
	return values
})

var ErrAccountPasswordWeak = errors.New("This password is too common. Choose a different password.")

func ValidateSecurityAccountPassword(password string) error {
	if err := ValidateNewAccountPassword(password); err != nil {
		return err
	}
	return validateSecurityPasswordBlocklist(password)
}

func validateSecurityPasswordBlocklist(password string) error {
	if _, found := securityCommonPasswordSet()[strings.ToLower(password)]; found {
		return ErrAccountPasswordWeak
	}
	return nil
}
