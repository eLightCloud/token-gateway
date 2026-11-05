package common

import (
	"errors"
	"net/mail"
	"regexp"
	"strings"
	"sync/atomic"
)

const EmailDeliveryOptionKey = "EmailDeliveryConfig"
const EmailAuthLegacy = "legacy"
const EmailAuthMicrosoft = "microsoft_oauth2_app"

var ErrEmailDeliveryUnavailable = errors.New("Email configuration is unavailable. Check the server encryption key and saved settings.")
var emailGUID = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

type LegacyEmailSettings struct {
	Host               string `json:"host"`
	Port               int    `json:"port"`
	Account            string `json:"account"`
	From               string `json:"from"`
	SSL                bool   `json:"ssl"`
	StartTLS           bool   `json:"starttls"`
	InsecureSkipVerify bool   `json:"insecure_skip_verify"`
	ForceLogin         bool   `json:"force_login"`
	Password           string `json:"-"`
}
type MicrosoftEmailSettings struct {
	TenantID     string `json:"tenant_id"`
	ClientID     string `json:"client_id"`
	Mailbox      string `json:"mailbox"`
	ClientSecret string `json:"-"`
}
type EmailDeliveryOptions struct {
	AuthMode  string                 `json:"auth_mode"`
	Legacy    LegacyEmailSettings    `json:"legacy"`
	Microsoft MicrosoftEmailSettings `json:"microsoft"`
}
type EmailDeliveryConfig struct {
	Revision int64                `json:"revision"`
	Options  EmailDeliveryOptions `json:"options"`
}
type emailDeliverySnapshot struct {
	config EmailDeliveryConfig
	err    error
}

type EmailDeliveryRuntime struct {
	state atomic.Pointer[emailDeliverySnapshot]
}

var DefaultEmailDelivery = &EmailDeliveryRuntime{}

// PublishEmailDelivery publishes whole revisions. A delayed refresh cannot undo a newer save.
func PublishEmailDelivery(config EmailDeliveryConfig, err error) {
	next := &emailDeliverySnapshot{config: config, err: err}
	for {
		old := DefaultEmailDelivery.state.Load()
		if old != nil && old.config.Revision > config.Revision && err == nil {
			return
		}
		if DefaultEmailDelivery.state.CompareAndSwap(old, next) {
			return
		}
	}
}

func ValidEmailDeliveryAddress(address string) bool {
	if strings.ContainsAny(address, "\r\n;,") {
		return false
	}
	parsed, err := mail.ParseAddress(address)
	return err == nil && parsed.Address == address && len(address) <= 254
}

// Validate checks an active configuration; inactive provider drafts can remain incomplete.
func (o EmailDeliveryOptions) Validate() error {
	if o.AuthMode == EmailAuthMicrosoft {
		if !emailGUID.MatchString(o.Microsoft.TenantID) || !emailGUID.MatchString(o.Microsoft.ClientID) || !ValidEmailDeliveryAddress(o.Microsoft.Mailbox) {
			return errors.New("Enter valid Microsoft tenant, application and mailbox details.")
		}
		return nil
	}
	if o.AuthMode != EmailAuthLegacy {
		return errors.New("Select a supported email authentication method.")
	}
	s := o.Legacy
	if strings.TrimSpace(s.Host) == "" || strings.ContainsAny(s.Host, "/\\\r\n\t :") || s.Port < 1 || s.Port > 65535 {
		return errors.New("Enter a valid SMTP host and port.")
	}
	from := s.From
	if from == "" {
		from = s.Account
	}
	if !ValidEmailDeliveryAddress(from) || strings.ContainsAny(s.Account, "\r\n") || (s.SSL && s.StartTLS) {
		return errors.New("Enter a valid SMTP sender and security mode.")
	}
	return nil
}
