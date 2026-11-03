package model

import (
	"fmt"
	"strings"
	"time"

	"github.com/pquerna/otp/totp"
	"gorm.io/gorm"
)

// A persistent assertion claim makes accepting a TOTP step atomic across
// login, step-up, instances and restarts. Expiry cleanup outlives the skew window.
func ClaimTwoFACode(factorID int, secret, code string) error {
	return claimTwoFACodeWithTx(DB, factorID, secret, code)
}

func claimTwoFACodeWithTx(tx *gorm.DB, factorID int, secret, code string) error {
	now := time.Now()
	for _, offset := range []time.Duration{0, -30 * time.Second, 30 * time.Second} {
		at := now.Add(offset)
		expected, err := totp.GenerateCode(secret, at)
		if err != nil {
			return err
		}
		if expected != strings.ReplaceAll(code, " ", "") {
			continue
		}
		return ClaimExternalAuthAssertionWithTx(tx, "totp_step", fmt.Sprintf("%d:%d:%s", factorID, at.Unix()/30, expected), now.Add(2*time.Minute))
	}
	return ErrTwoFACodeInvalid
}
