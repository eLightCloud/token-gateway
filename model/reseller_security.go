package model

import "gorm.io/gorm"

// completeSecurityFactorEnrollment joins the MFA transaction so replay claims,
// reseller activation, and the factor itself either all commit or all roll back.
func completeSecurityFactorEnrollment(tx *gorm.DB, userID int, factor *TwoFA, code string) error {
	kind, err := ResellerUserKind(tx, userID)
	if err != nil {
		return err
	}
	if SecurityCompletionRequired(kind) {
		if err := claimTwoFACodeWithTx(tx, factor.Id, factor.Secret, code); err != nil {
			return err
		}
	}
	if kind == "" {
		return nil
	}
	return tx.Model(&ResellerIdentity{}).Where("user_id = ? AND activation = ?", userID, "security_pending").Update("activation", "active").Error
}
