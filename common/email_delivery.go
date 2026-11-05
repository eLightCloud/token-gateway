package common

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/smtp"
	"net/textproto"
	"slices"
	"strings"
	"time"
)

type EmailDeliveryStage struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}
type EmailDeliveryReport struct {
	Revision  int64                `json:"revision"`
	Timestamp int64                `json:"timestamp"`
	RequestID string               `json:"request_id"`
	Stages    []EmailDeliveryStage `json:"stages"`
	Code      string               `json:"code"`
	Reason    string               `json:"reason,omitempty"`
}

// DeliverConfiguredEmail is shared by production and the saved-configuration test API.
// No SMTP error text escapes this boundary: AUTH/DATA replies may contain secrets.
func DeliverConfiguredEmail(ctx context.Context, config EmailDeliveryConfig, subject, receiver, content string) (report EmailDeliveryReport, resultErr error) {
	report = EmailDeliveryReport{Revision: config.Revision, Timestamp: time.Now().Unix(), RequestID: GetRandomString(20), Code: "EMAIL_DELIVERY_FAILED"}
	for _, name := range []string{"config", "token", "connect_tls", "auth", "submit"} {
		report.Stages = append(report.Stages, EmailDeliveryStage{Name: name, Status: "skipped"})
	}
	stage := 0
	defer func() {
		if resultErr != nil && report.Stages[stage].Status != "unknown" {
			report.Stages[stage].Status = "failed"
		}
		if resultErr != nil {
			resultErr = errors.New("Email delivery failed at " + report.Stages[stage].Name + ". Check saved settings and provider permissions.")
		}
	}()
	if err := config.Options.Validate(); err != nil {
		return report, err
	}
	if strings.ContainsAny(subject, "\r\n") {
		return report, errors.New("invalid email subject")
	}
	recipients := strings.Split(receiver, ";")
	for _, recipient := range recipients {
		if !ValidEmailDeliveryAddress(recipient) {
			return report, errors.New("invalid email recipient")
		}
	}
	report.Stages[0].Status = "passed"
	settings := config.Options.Legacy
	microsoft := config.Options.AuthMode == EmailAuthMicrosoft
	var token string
	if microsoft {
		stage = 1
		if config.Options.Microsoft.ClientSecret == "" {
			return report, errors.New("missing Microsoft credentials")
		}
		var err error
		token, err = microsoftEmailToken(config, false)
		if err != nil {
			return report, err
		}
		report.Stages[1].Status = "passed"
		settings = LegacyEmailSettings{Host: "smtp.office365.com", Port: 587, Account: config.Options.Microsoft.Mailbox, From: config.Options.Microsoft.Mailbox, StartTLS: true}
	}
	if settings.From == "" {
		settings.From = settings.Account
	}
	for attempt := range 2 {
		stage = 2
		client, err := dialConfiguredSMTP(ctx, settings)
		if err != nil {
			return report, err
		}
		report.Stages[2].Status = "passed"
		stage = 3
		if microsoft || (settings.Account != "" && settings.Password != "") {
			var auth smtp.Auth
			if microsoft {
				auth = &microsoftEmailAuth{mailbox: settings.Account, token: token}
			} else {
				auth = &smtpAutoAuth{username: settings.Account, password: settings.Password, configured: true, server: settings.Host, forceLogin: settings.ForceLogin, preferLogin: isOutlookServer(settings.Account) || slices.Contains(EmailLoginAuthServerList, settings.Host)}
			}
			err = client.Auth(auth)
			if err != nil {
				_ = client.Close()
				var smtpError *textproto.Error
				if microsoft && errors.As(err, &smtpError) && strings.Contains(strings.ToLower(smtpError.Msg), "smtpclientauthentication is disabled") {
					report.Reason = "SMTP_AUTH_DISABLED"
					return report, err
				}
				if microsoft && attempt == 0 && errors.As(err, &smtpError) && smtpError.Code == 535 {
					token, err = microsoftEmailToken(config, true)
					if err != nil {
						stage = 1
						return report, err
					}
					continue
				}
				return report, err
			}
			report.Stages[3].Status = "passed"
		}
		stage = 4
		err = submitConfiguredEmail(client, settings.From, subject, recipients, content, &report)
		_ = client.Close()
		if err != nil {
			return report, err
		}
		report.Stages[4].Status = "passed"
		report.Code = "EMAIL_ACCEPTED"
		return report, nil
	}
	return report, errors.New("SMTP authentication failed")
}

func dialConfiguredSMTP(ctx context.Context, s LegacyEmailSettings) (*smtp.Client, error) {
	conn, err := (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(s.Host, fmt.Sprint(s.Port)))
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(30 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	if err = conn.SetDeadline(deadline); err != nil {
		_ = conn.Close()
		return nil, err
	}
	tlsConfig := &tls.Config{ServerName: s.Host, MinVersion: tls.VersionTLS12, InsecureSkipVerify: s.InsecureSkipVerify} // #nosec G402 -- legacy admin compatibility; Microsoft settings always verify.
	if s.SSL || (s.Port == 465 && !s.StartTLS) {
		secure := tls.Client(conn, tlsConfig)
		if err = secure.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return nil, err
		}
		conn = secure
	}
	client, err := smtp.NewClient(conn, s.Host)
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if s.StartTLS {
		if ok, _ := client.Extension("STARTTLS"); !ok {
			_ = client.Close()
			return nil, errors.New("STARTTLS unavailable")
		}
		if err = client.StartTLS(tlsConfig); err != nil {
			_ = client.Close()
			return nil, err
		}
	}
	return client, nil
}

func submitConfiguredEmail(client *smtp.Client, from, subject string, to []string, content string, report *EmailDeliveryReport) error {
	if err := client.Mail(from); err != nil {
		return err
	}
	for _, recipient := range to {
		if err := client.Rcpt(recipient); err != nil {
			return err
		}
	}
	w, err := client.Data()
	if err != nil {
		return err
	}
	report.Stages[4].Status = "unknown"
	report.Code = "EMAIL_DELIVERY_UNKNOWN"
	_, domain, _ := strings.Cut(from, "@")
	message := fmt.Sprintf("To: %s\r\nFrom: =?UTF-8?B?%s?= <%s>\r\nSubject: =?UTF-8?B?%s?=\r\nDate: %s\r\nMessage-ID: <%s@%s>\r\nContent-Type: text/html; charset=UTF-8\r\n\r\n%s\r\n", strings.Join(to, ", "), base64.StdEncoding.EncodeToString([]byte(SystemName)), from, base64.StdEncoding.EncodeToString([]byte(subject)), time.Now().Format(time.RFC1123Z), GetRandomString(24), domain, content)
	if _, err = io.WriteString(w, message); err != nil {
		return err
	}
	if err = w.Close(); err != nil {
		var reply *textproto.Error
		if errors.As(err, &reply) {
			report.Stages[4].Status = "failed"
			report.Code = "EMAIL_DELIVERY_FAILED"
		}
		return err
	}
	// DATA accepted is authoritative even if QUIT subsequently fails.
	_ = client.Quit()
	return nil
}
