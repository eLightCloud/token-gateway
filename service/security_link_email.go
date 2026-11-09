package service

import (
	"fmt"
	"html"
	"net/url"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/system_setting"
)

func DeliverSecurityMail(userID int, purpose, email, token, lang string) error {
	return deliverSecurityMail(userID, purpose, email, token, "", lang)
}

func deliverSecurityMail(userID int, purpose, email, token, agentUsername, lang string) error {
	lang = i18n.ParseAcceptLanguage(lang)
	if lang == i18n.LangZhTW {
		lang = i18n.LangZhCN
	}

	kind, prefix := common.EmailSecuritySetup, "security_mail.setup."
	if purpose == model.SecurityPasswordPurpose {
		kind, prefix = common.EmailPasswordRecovery, "security_mail.recovery."
	}
	translate := func(key string) string { return i18n.Translate(lang, key) }
	subject := translate(prefix + "subject")
	intro := translate(prefix + "intro")
	expiry := i18n.Translate(lang, "security_mail.expiry", map[string]any{"Minutes": int(model.SecurityMailTTL / time.Minute)})
	// Fragment tokens stay out of HTTP access logs and Referer headers.
	link := system_setting.ServerAddress + "/account-activation#" + url.Values{"token": {token}, "user_id": {fmt.Sprint(userID)}, "purpose": {purpose}}.Encode()
	content := fmt.Sprintf(`<p>%s</p><p>%s</p><p><a href="%s">%s</a></p>`, html.EscapeString(intro), html.EscapeString(expiry), html.EscapeString(link), html.EscapeString(translate(prefix+"action")))
	if agentUsername != "" {
		subject = translate("security_mail.agent.subject")
		username := i18n.Translate(lang, "security_mail.agent.username", map[string]any{"Username": agentUsername})
		content = fmt.Sprintf(`<p>%s</p><p><a href="%s">%s</a></p><p>%s</p>`,
			html.EscapeString(username),
			html.EscapeString(system_setting.ServerAddress+"/sign-in"), html.EscapeString(translate("security_mail.agent.sign_in")),
			html.EscapeString(translate("security_mail.agent.activation_required"))) + content
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
	if err := common.SendSecurityLinkEmail(kind, subject, email, content, copy); err != nil {
		_, _ = model.ConsumeAuthFlow(token, model.AuthFlowMatch{Purpose: purpose, UserId: userID})
		return ErrEmailBindingDelivery
	}
	return nil
}
