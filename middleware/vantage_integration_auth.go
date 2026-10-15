package middleware

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"

	"github.com/gin-gonic/gin"
)

// VantageIntegrationAuth 逐请求校验 Vantage 专用接入口令与来源限制。
//
// 口令与模型调用 Token 是两种凭据：前者不能调用模型或获得后台管理员
// 权限，后者不能调用接入管理接口。本中间件只做接入身份这一层；
// 对象范围与状态由各端点在服务端解析，不信任客户端指定。
//
// 来源仅取 TCP 对端 IPv4，不读取代理头或继承全局代理信任策略。
// 共享出口不能替代应用口令；两者必须同时成立。
func VantageIntegrationAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		config, err := model.GetVantageIntegrationConfig()
		if err != nil {
			common.SysError("failed to load Vantage integration settings")
			vantageAbort(c, http.StatusServiceUnavailable, "configuration_unavailable", "integration settings are unavailable")
			return
		}
		if !config.Enabled {
			vantageAbort(c, http.StatusServiceUnavailable, "integration_disabled", "integration is disabled")
			return
		}
		peer, _, peerErr := net.SplitHostPort(c.Request.RemoteAddr)
		ip, ipErr := netip.ParseAddr(peer)
		presented, hasBearer := strings.CutPrefix(c.GetHeader("Authorization"), "Bearer ")
		if !hasBearer || presented == "" || !config.SecretMatches(presented) {
			// 低敏拒绝事件：只记录来源 IP 与原因，不记录所提交的口令内容。
			common.SysLog(fmt.Sprintf("vantage integration auth denied: invalid credential from %s request_id=%s", peer, c.GetString(common.RequestIdKey)))
			c.Header("WWW-Authenticate", `Bearer realm="vantage-integration"`)
			vantageAbort(c, http.StatusUnauthorized, "invalid_credential", "invalid integration credential")
			return
		}
		if peerErr != nil || ipErr != nil || !config.SourceAllowed(ip) {
			common.SysLog(fmt.Sprintf("vantage integration auth denied: source %s not allowed request_id=%s", peer, c.GetString(common.RequestIdKey)))
			vantageAbort(c, http.StatusForbidden, "source_not_allowed", "source is not allowed")
			return
		}
		// 状态及秘密交付响应一律不可缓存，密钥不进入共享缓存或代理。
		c.Header("Cache-Control", "no-store")
		c.Next()
	}
}

func vantageAbort(c *gin.Context, status int, code string, message string) {
	c.Header("Cache-Control", "no-store")
	c.AbortWithStatusJSON(status, gin.H{
		"success":    false,
		"code":       code,
		"message":    message,
		"request_id": c.GetString(common.RequestIdKey),
	})
}
