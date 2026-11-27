package middleware

import (
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"slices"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

// AgentActivationTransport permits direct HTTPS, configured TLS termination,
// and local development on loopback. The proxy must overwrite the scheme header.
func AgentActivationTransport(c *gin.Context) (secure bool, allowed bool) {
	public, err := service.AgentActivationOrigin()
	if err != nil {
		return false, false
	}
	request := c.Request
	if request.TLS != nil {
		origin, err := common.NormalizeOrigin("https://" + request.Host)
		browserOrigin, validOrigin := requestBrowserOrigin(request)
		return true, err == nil && origin == public && validOrigin && browserOrigin == public
	}
	peer, _, err := net.SplitHostPort(request.RemoteAddr)
	if err != nil {
		return false, false
	}
	if strings.HasPrefix(public, "http://") {
		if !net.ParseIP(peer).IsLoopback() {
			return false, false
		}
		target, err := url.Parse("http://" + request.Host)
		if err != nil || (target.Hostname() != "localhost" && !net.ParseIP(target.Hostname()).IsLoopback()) {
			return false, false
		}
		// The frontend dev proxy changes Host to the backend port. Its browser
		// Origin still has to match the configured loopback public address.
		browserOrigin, validOrigin := requestBrowserOrigin(request)
		return false, validOrigin && browserOrigin == public
	}

	forwarded := request.Header.Values("X-Forwarded-Proto")
	browserOrigin, validOrigin := requestBrowserOrigin(request)
	if len(forwarded) != 1 || forwarded[0] != "https" || !validOrigin || browserOrigin != public ||
		!common.SessionCookieSecure || !slices.Contains(common.SessionCookieTrustedURLs, public) {
		return false, false
	}
	addr, err := netip.ParseAddr(peer)
	if err != nil {
		return false, false
	}
	trusted, _, err := common.ResolveTrustedProxies(os.Getenv("TRUSTED_PROXIES"))
	if err != nil {
		return false, false
	}
	for _, entry := range trusted {
		if prefix, err := netip.ParsePrefix(entry); err == nil && prefix.Contains(addr.Unmap()) {
			return true, true
		}
		if proxy, err := netip.ParseAddr(entry); err == nil && proxy.Unmap() == addr.Unmap() {
			return true, true
		}
	}
	return false, false
}

func RequireAgentActivationTransport(c *gin.Context) {
	if _, allowed := AgentActivationTransport(c); !allowed {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
			"success": false, "code": "AGENT_ACTIVATION_HTTPS_REQUIRED", "message": "Use the secure HTTPS address to activate the reseller account.",
		})
		return
	}
	c.Next()
}
