package common

import (
	"bytes"
	"context"
	"errors"
	"io"
	"maps"
	"net"
	"net/http"
	"net/mail"
	"strings"
	"time"
)

var resendEmailClient = &http.Client{
	Timeout: 20 * time.Second,
	CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

// Protected by OptionMapRWMutex along with the selected provider and credentials.
var resendCredentialUnavailable bool

func PublishResendDeliveryOptions(values map[string]string, credentialUnavailable bool) {
	OptionMapRWMutex.Lock()
	defer OptionMapRWMutex.Unlock()
	if _, ok := values["ResendApiKey"]; ok {
		resendCredentialUnavailable = credentialUnavailable
	}
	maps.Copy(OptionMap, values)
}

// ValidateResendConfiguration is shared by provider activation and delivery.
// It checks local configuration only; saving settings never sends an email.
func ValidateResendConfiguration(apiKey, from string) error {
	apiKey = strings.TrimSpace(apiKey)
	if apiKey == "" || strings.ContainsAny(apiKey, "\r\n\x00") {
		return NewMessage("Resend API key is not configured or invalid")
	}
	if strings.ContainsAny(from, "\r\n\x00") {
		return NewMessage("Resend sender address is not configured or invalid")
	}
	if _, err := mail.ParseAddress(strings.TrimSpace(from)); err != nil {
		return NewMessage("Resend sender address is not configured or invalid")
	}
	return nil
}

// API contract: https://resend.com/docs/api-reference/emails/send-email
// OWASP Authentication / Session Management / Forgot Password cheat sheets:
// do not expose credentials, verification codes or reset links in errors/logs.
// This transport does not change token expiry, single-use or session rules.
func sendResendEmail(apiKey, from, subject, receiver, content string) error {
	if err := ValidateResendConfiguration(apiKey, from); err != nil {
		kind := EmailDeliveryConfiguration
		if strings.TrimSpace(apiKey) == "" || strings.TrimSpace(from) == "" {
			kind = EmailDeliveryNotConfigured
		}
		return emailDeliveryFailure{kind, err}
	}
	apiKey = strings.TrimSpace(apiKey)
	sender, _ := mail.ParseAddress(strings.TrimSpace(from))
	if sender.Name == "" {
		sender.Name = SystemName
	}
	to := make([]string, 0)
	for address := range strings.SplitSeq(receiver, ";") {
		recipient, err := mail.ParseAddress(strings.TrimSpace(address))
		if err != nil || len(to) >= 50 {
			return NewMessage("Invalid email address")
		}
		to = append(to, recipient.Address)
	}
	payload, err := Marshal(struct {
		From    string   `json:"from"`
		To      []string `json:"to"`
		Subject string   `json:"subject"`
		HTML    string   `json:"html"`
	}{sender.String(), to, subject, content})
	if err != nil {
		return NewMessage("Resend request failed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.resend.com/emails", bytes.NewReader(payload))
	if err != nil {
		return NewMessage("Resend request failed")
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := resendEmailClient.Do(req)
	if err != nil {
		failure := NewMessage("Resend request failed")
		var connection *net.OpError
		if errors.As(err, &connection) && connection.Op == "dial" {
			return emailDeliveryFailure{EmailDeliveryConnection, failure}
		}
		return failure
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		SysError(LogText("Resend rejected the email: status=%d", resp.StatusCode))
		failure := NewMessage("Resend email delivery failed (HTTP {{status}})", map[string]any{"status": resp.StatusCode})
		if resp.StatusCode == http.StatusUnauthorized ||
			(resp.StatusCode == http.StatusForbidden && resendCredentialRejected(resp.Body)) {
			return emailDeliveryFailure{EmailDeliveryAuthentication, failure}
		}
		if resp.StatusCode >= 400 && resp.StatusCode < 500 && resp.StatusCode != http.StatusTooManyRequests {
			return emailDeliveryFailure{EmailDeliveryRejected, failure}
		}
		return failure
	}
	var result struct {
		ID string `json:"id"`
	}
	if err := DecodeJson(io.LimitReader(resp.Body, 4096), &result); err != nil || strings.TrimSpace(result.ID) == "" {
		return NewMessage("Invalid Resend delivery response")
	}
	return nil
}

func resendCredentialRejected(body io.Reader) bool {
	var diagnostic struct {
		Name string `json:"name"`
	}
	if DecodeJson(io.LimitReader(body, 4096), &diagnostic) != nil {
		return false
	}
	switch diagnostic.Name {
	case "restricted_api_key", "suspended_api_key", "invalid_permission":
		return true
	}
	return false
}
