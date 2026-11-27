package service

import (
	"errors"
	"fmt"
	"html"
	"net/url"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/system_setting"
)

func SendResellerActivationMail(userID int, expectedEmail, lang string) error {
	user, err := model.GetUserById(userID, false)
	if err != nil {
		return err
	}
	if expectedEmail != "" && model.NormalizeEmail(user.Email) != expectedEmail {
		return model.ErrResellerActivationAddressChanged
	}
	email, err := ValidateAccountEmail(user.Email)
	if err != nil {
		return err
	}
	token, err := model.NewPendingResellerActivationMail(userID, email)
	if err != nil {
		return err
	}
	if err := deliverResellerActivationLink(userID, model.SecurityActivationPurpose, email, token, user.Username, user.ResellerKind, lang); err != nil {
		// The pending purpose already prevents redemption if this cleanup fails.
		if cleanupErr := model.DiscardPendingResellerActivationMail(userID, token); cleanupErr != nil &&
			!errors.Is(cleanupErr, model.ErrAuthFlowConsumed) && !errors.Is(cleanupErr, model.ErrAuthFlowExpired) {
			common.SysError(common.LogText("Could not clean up pending reseller activation link: user_id=%d", userID))
		}
		return err
	}
	return model.ConfirmResellerActivationMail(userID, email, token)
}

func deliverResellerActivationLink(userID int, purpose, email, token, username, accountKind, lang string) error {
	lang = i18n.ParseAcceptLanguage(lang)
	if lang == i18n.LangZhTW {
		lang = i18n.LangZhCN
	}

	prefix := "security_mail.setup."
	agentActivation := purpose == model.SecurityActivationPurpose && accountKind == "agent"
	if agentActivation {
		prefix = "security_mail.agent_activation."
	}
	if purpose == model.SecurityAgentResumePurpose {
		prefix = "security_mail.agent_resume."
	}
	translate := func(key string) string { return i18n.Translate(lang, key) }
	subject := translate(prefix + "subject")
	intro := translate(prefix + "intro")
	expiry := i18n.Translate(lang, "security_mail.expiry", map[string]any{"Minutes": int(model.SecurityMailTTL / time.Minute)})
	// Fragment tokens stay out of HTTP access logs and Referer headers.
	params := url.Values{"token": {token}, "user_id": {fmt.Sprint(userID)}, "purpose": {purpose}}
	if agentActivation {
		params.Set("account", "agent")
	}
	origin := system_setting.ServerAddress
	if agentActivation || purpose == model.SecurityAgentResumePurpose {
		var err error
		origin, err = AgentActivationOrigin()
		if err != nil {
			if purpose != model.SecurityActivationPurpose {
				_, _ = model.ConsumeAuthFlow(token, model.AuthFlowMatch{Purpose: purpose, UserId: userID})
			}
			return err
		}
	}
	link := origin + "/account-activation#" + params.Encode()
	content := fmt.Sprintf(`<p>%s</p><p>%s</p><p><a href="%s">%s</a></p>`, html.EscapeString(intro), html.EscapeString(expiry), html.EscapeString(link), html.EscapeString(translate(prefix+"action")))
	if agentActivation {
		usernameLine := i18n.Translate(lang, "security_mail.agent.username", map[string]any{"Username": username})
		content = "<p>" + html.EscapeString(usernameLine) + "</p>" + content
	} else if username != "" && accountKind == "agent_customer" {
		subject = translate("security_mail.customer.subject")
		usernameLine := i18n.Translate(lang, "security_mail.agent.username", map[string]any{"Username": username})
		content = fmt.Sprintf(`<p>%s</p><p><a href="%s">%s</a></p><p>%s</p>`,
			html.EscapeString(usernameLine),
			html.EscapeString(system_setting.ServerAddress+"/sign-in"), html.EscapeString(translate("security_mail.agent.sign_in")),
			html.EscapeString(translate("security_mail.customer.activation_required"))) + content
	}
	copy := common.SecurityLinkEmailCopy{
		Language:  lang,
		Label:     translate(prefix + "label"),
		Preview:   intro,
		Heading:   translate(prefix + "heading"),
		Warning:   translate(prefix + "warning"),
		Footer:    translate("security_mail.footer"),
		PoweredBy: translate("security_mail.powered_by"),
	}
	if err := common.SendSecurityLinkEmail(common.EmailSecuritySetup, subject, email, content, copy); err != nil {
		if purpose == model.SecurityActivationPurpose {
			return &ResellerActivationDeliveryError{Kind: common.ClassifyEmailDeliveryFailure(err)}
		}
		_, _ = model.ConsumeAuthFlow(token, model.AuthFlowMatch{Purpose: purpose, UserId: userID})
		return ErrEmailBindingDelivery
	}
	return nil
}
