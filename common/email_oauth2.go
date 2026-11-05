package common

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"
)

type microsoftTokenBody struct {
	io.Reader
	io.Closer
}
type microsoftTokenTransport struct{ base http.RoundTripper }

func (t microsoftTokenTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.URL.Scheme != "https" || r.URL.Host != "login.microsoftonline.com" {
		return nil, errors.New("invalid Microsoft token endpoint")
	}
	response, err := t.base.RoundTrip(r)
	if err == nil {
		response.Body = microsoftTokenBody{io.LimitReader(response.Body, 64<<10), response.Body}
	}
	return response, err
}

var microsoftTokens struct {
	sync.Mutex
	fingerprint [32]byte
	source      oauth2.TokenSource
}

func microsoftEmailToken(config EmailDeliveryConfig, refresh bool) (string, error) {
	// Serialize refreshes and cache only one active configuration; old requests keep their own token.
	microsoftTokens.Lock()
	defer microsoftTokens.Unlock()
	fields, _ := Marshal(config)
	fingerprint := sha256.Sum256(append(fields, []byte(config.Options.Microsoft.ClientSecret)...))
	if refresh || microsoftTokens.source == nil || microsoftTokens.fingerprint != fingerprint {
		client := &http.Client{Timeout: 15 * time.Second, Transport: microsoftTokenTransport{http.DefaultTransport}, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
		ctx := context.WithValue(context.Background(), oauth2.HTTPClient, client)
		settings := config.Options.Microsoft
		credentials := clientcredentials.Config{ClientID: settings.ClientID, ClientSecret: settings.ClientSecret, TokenURL: "https://login.microsoftonline.com/" + settings.TenantID + "/oauth2/v2.0/token", Scopes: []string{"https://outlook.office365.com/.default"}, AuthStyle: oauth2.AuthStyleInParams}
		microsoftTokens.source = credentials.TokenSource(ctx)
		microsoftTokens.fingerprint = fingerprint
	}
	token, err := microsoftTokens.source.Token()
	if err != nil || token == nil || token.AccessToken == "" || !strings.EqualFold(token.TokenType, "Bearer") || token.Expiry.IsZero() || time.Until(token.Expiry) <= 0 {
		// oauth2.RetrieveError may contain echoed credentials or a usable token. Never propagate it.
		return "", errors.New("Microsoft token request failed. Check application credentials, consent and network access.")
	}
	return token.AccessToken, nil
}
