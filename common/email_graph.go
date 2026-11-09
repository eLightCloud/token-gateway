package common

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"
)

const microsoftGraphOrigin = "https://graph.microsoft.com"

func submitMicrosoftGraphEmail(ctx context.Context, config EmailDeliveryConfig, token, subject string, recipients []string, content string, report *EmailDeliveryReport) error {
	addresses := make([]map[string]map[string]string, 0, len(recipients))
	for _, recipient := range recipients {
		addresses = append(addresses, map[string]map[string]string{"emailAddress": {"address": recipient}})
	}
	payload, err := Marshal(map[string]any{
		"message": map[string]any{
			"subject":      subject,
			"body":         map[string]string{"contentType": "HTML", "content": content},
			"toRecipients": addresses,
		},
		"saveToSentItems": false,
	})
	if err != nil {
		return err
	}
	endpoint := microsoftGraphOrigin + "/v1.0/users/" + url.PathEscape(config.Options.Microsoft.Mailbox) + "/sendMail"
	client := &http.Client{
		Timeout: 20 * time.Second,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	for attempt := range 2 {
		if err := ctx.Err(); err != nil {
			return err
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
		if err != nil {
			return err
		}
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Content-Type", "application/json")
		response, err := client.Do(request)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusAccepted {
				return nil
			}
			if response.StatusCode == http.StatusUnauthorized && attempt == 0 {
				// Graph explicitly rejected the token before accepting the message.
				token, err = microsoftEmailToken(ctx, config, true)
				if err != nil {
					return err
				}
				continue
			}
			if response.StatusCode == http.StatusForbidden {
				report.Reason = "GRAPH_ACCESS_DENIED"
			}
			if response.StatusCode < http.StatusInternalServerError {
				return errors.New("Microsoft Graph rejected the email")
			}
		}
		// A disconnect or server error can follow acceptance. Never replay it.
		report.Stages[len(report.Stages)-1].Status = "unknown"
		report.Code = "EMAIL_DELIVERY_UNKNOWN"
		return errors.New("Microsoft Graph submission outcome is unknown")
	}
	return errors.New("Microsoft Graph rejected the email")
}
