package router

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestAgentActivationTransportOnRegisteredRoutes(t *testing.T) {
	t.Setenv("TRUSTED_PROXIES", "10.20.0.3")
	t.Setenv("SECURITY_COMPLETION_ENFORCED", "true")
	prevAddress := system_setting.ServerAddress
	prevSecure, prevOrigins := common.SessionCookieSecure, common.SessionCookieTrustedURLs
	common.SessionCookieSecure = true
	common.SessionCookieTrustedURLs = []string{"https://example.com"}
	t.Cleanup(func() {
		system_setting.ServerAddress = prevAddress
		common.SessionCookieSecure, common.SessionCookieTrustedURLs = prevSecure, prevOrigins
	})
	engine := gin.New()
	registerSecurityCompletionRoutes(engine.Group("/api"))
	const path = "/api/security/agent-activation/mfa"
	for _, tc := range []struct {
		name, address, transport, origin, peer, forwarded, flowID string
		status                                                    int
		code                                                      string
	}{
		{"direct-tls", "https://example.com", "https://example.com", "https://example.com", "203.0.113.1", "", "42", http.StatusBadRequest, "AUTH_FLOW_INVALID"},
		{"trusted-tls-proxy", "https://example.com", "http://internal.example", "https://example.com", "10.20.0.3", "https", "42", http.StatusBadRequest, "AUTH_FLOW_INVALID"},
		{"public-http", "http://example.com", "http://example.com", "http://example.com", "203.0.113.1", "", "42", http.StatusForbidden, "AGENT_ACTIVATION_HTTPS_REQUIRED"},
		{"untrusted-peer", "https://example.com", "http://example.com", "https://example.com", "203.0.113.1", "https", "42", http.StatusForbidden, "AGENT_ACTIVATION_HTTPS_REQUIRED"},
		{"cross-origin", "https://example.com", "https://example.com", "https://other.example", "203.0.113.1", "", "42", http.StatusForbidden, "AGENT_ACTIVATION_HTTPS_REQUIRED"},
		{"missing-flow-header", "https://example.com", "https://example.com", "https://example.com", "203.0.113.1", "", "", http.StatusForbidden, ""},
		{"local-development", "http://localhost:3000", "http://localhost:3000", "http://localhost:3000", "127.0.0.1", "", "42", http.StatusBadRequest, "AUTH_FLOW_INVALID"},
		{"local-development-proxy", "http://localhost:3201", "http://localhost:3200", "http://localhost:3201", "127.0.0.1", "", "42", http.StatusBadRequest, "AUTH_FLOW_INVALID"},
		{"local-development-foreign-origin", "http://localhost:3201", "http://localhost:3200", "http://localhost:9090", "127.0.0.1", "", "42", http.StatusForbidden, "AGENT_ACTIVATION_HTTPS_REQUIRED"},
		{"local-development-public-peer", "http://localhost:3201", "http://localhost:3200", "http://localhost:3201", "203.0.113.1", "", "42", http.StatusForbidden, "AGENT_ACTIVATION_HTTPS_REQUIRED"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			system_setting.ServerAddress = tc.address
			request := httptest.NewRequest(http.MethodGet, tc.transport+path, nil)
			request.RemoteAddr = tc.peer + ":12345"
			request.Header.Set("Origin", tc.origin)
			if tc.forwarded != "" {
				request.Header.Set("X-Forwarded-Proto", tc.forwarded)
			}
			if tc.flowID != "" {
				request.Header.Set("X-Agent-Activation", tc.flowID)
			}
			response := httptest.NewRecorder()
			engine.ServeHTTP(response, request)
			assert.Equal(t, tc.status, response.Code)
			assert.Contains(t, response.Header().Get("Cache-Control"), "no-store")
			if tc.code != "" {
				assert.Contains(t, response.Body.String(), tc.code)
			}
		})
	}
}
