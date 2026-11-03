package service

import (
	"fmt"
	"html"
	"net/url"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/system_setting"
)

func SendSecurityCompletionMail(userID int, purpose string, identity *AuthIdentity) error {
	user, err := model.GetUserById(userID, false)
	if err != nil {
		return err
	}
	email, err := ValidateAccountEmail(user.Email)
	if err != nil {
		return err
	}
	token, err := model.NewSecurityMailFlow(userID, purpose, email, identity)
	if err != nil {
		return err
	}
	return DeliverSecurityMail(userID, purpose, email, token)
}

func DeliverSecurityMail(userID int, purpose, email, token string) error {
	// Fragment tokens stay out of HTTP access logs and Referer headers.
	link := system_setting.ServerAddress + "/account-activation#" + url.Values{"token": {token}, "user_id": {fmt.Sprint(userID)}, "purpose": {purpose}}.Encode()
	content := "<p>Verify your email and complete account security setup. This link expires in 10 minutes.</p><p><a href=\"" + html.EscapeString(link) + "\">Continue security setup</a></p>"
	if err := common.SendEmail("Account security setup", email, content); err != nil {
		_, _ = model.ConsumeAuthFlow(token, model.AuthFlowMatch{Purpose: purpose, UserId: userID})
		return ErrEmailBindingDelivery
	}
	return nil
}

// Verification ceremonies are part of enrollment, not a general-purpose
// bypass into the rest of the security center while the account is restricted.
func CheckSecurityCompletionVerification(userID int, scope string) error {
	state, err := model.GetSecurityCompletion(userID)
	if err != nil {
		return err
	}
	if !state.Required || state.Complete {
		return nil
	}
	if scope == VerificationScopeAccountBind {
		return nil
	}
	if scope == VerificationScopeTwoFASetup && state.EmailVerified {
		return nil
	}
	return ErrVerificationForbidden
}
