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

// A refresh is shared only while callers are waiting. Its HTTP request is cancelled
// when the last caller leaves, without letting one caller cancel other callers.
type microsoftTokenRefresh struct {
	fingerprint [32]byte
	done        chan struct{}
	cancel      context.CancelFunc
	waiters     int
	finished    bool
	token       *oauth2.Token
	err         error
}

var microsoftTokens struct {
	sync.Mutex
	revision    int64
	fingerprint [32]byte
	token       *oauth2.Token
	refresh     *microsoftTokenRefresh
}

func microsoftEmailToken(ctx context.Context, config EmailDeliveryConfig, forceRefresh bool) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	fields, err := Marshal(config)
	if err != nil {
		return "", ErrEmailDeliveryUnavailable
	}
	fingerprint := sha256.Sum256(append(fields, []byte(config.Options.Microsoft.ClientSecret)...))
	microsoftTokens.Lock()
	if config.Revision >= microsoftTokens.revision && microsoftTokens.fingerprint != fingerprint {
		microsoftTokens.revision, microsoftTokens.fingerprint, microsoftTokens.token = config.Revision, fingerprint, nil
	}
	if forceRefresh && microsoftTokens.fingerprint == fingerprint {
		microsoftTokens.token = nil
	}
	if microsoftTokens.fingerprint == fingerprint && microsoftTokens.token.Valid() {
		token := microsoftTokens.token.AccessToken
		microsoftTokens.Unlock()
		return token, nil
	}
	pending := microsoftTokens.refresh
	if pending == nil || pending.fingerprint != fingerprint {
		refreshContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
		pending = &microsoftTokenRefresh{fingerprint: fingerprint, done: make(chan struct{}), cancel: cancel}
		microsoftTokens.refresh = pending
		go func() {
			token, requestErr := requestMicrosoftEmailToken(refreshContext, config)
			microsoftTokens.Lock()
			pending.token, pending.err, pending.finished = token, requestErr, true
			if microsoftTokens.refresh == pending {
				if requestErr == nil && microsoftTokens.fingerprint == fingerprint {
					microsoftTokens.token = token
				}
				microsoftTokens.refresh = nil
			}
			close(pending.done)
			microsoftTokens.Unlock()
			cancel()
		}()
	}
	pending.waiters++
	microsoftTokens.Unlock()
	defer func() {
		microsoftTokens.Lock()
		pending.waiters--
		if pending.waiters == 0 && !pending.finished {
			pending.cancel()
			if microsoftTokens.refresh == pending {
				microsoftTokens.refresh = nil
			}
		}
		microsoftTokens.Unlock()
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-pending.done:
		if pending.err != nil {
			return "", pending.err
		}
		return pending.token.AccessToken, nil
	}
}

func requestMicrosoftEmailToken(ctx context.Context, config EmailDeliveryConfig) (*oauth2.Token, error) {
	client := &http.Client{Timeout: 15 * time.Second, Transport: microsoftTokenTransport{http.DefaultTransport}, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}
	ctx = context.WithValue(ctx, oauth2.HTTPClient, client)
	settings := config.Options.Microsoft
	credentials := clientcredentials.Config{ClientID: settings.ClientID, ClientSecret: settings.ClientSecret, TokenURL: "https://login.microsoftonline.com/" + settings.TenantID + "/oauth2/v2.0/token", Scopes: []string{microsoftGraphOrigin + "/.default"}, AuthStyle: oauth2.AuthStyleInParams}
	token, err := credentials.Token(ctx)
	if err != nil || token == nil || token.AccessToken == "" || !strings.EqualFold(token.TokenType, "Bearer") || token.Expiry.IsZero() || time.Until(token.Expiry) <= 0 {
		// RetrieveError can contain echoed credentials or usable tokens. Never propagate it.
		return nil, errors.New("Microsoft token request failed. Check application credentials, consent and network access.")
	}
	return token, nil
}
