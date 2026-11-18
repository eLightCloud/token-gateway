package middleware

import "github.com/gin-gonic/gin"

const resellerSelfAccount = "reseller_self_account"

func IsResellerSelfAccount(c *gin.Context) bool {
	return c.GetBool(resellerSelfAccount)
}

// The authenticated route pattern keeps parameterized self-service operations
// scoped to their controllers. Each controller still checks resource ownership.
func allowResellerSelfAccount(c *gin.Context) bool {
	switch c.Request.Method + " " + c.FullPath() {
	case "PUT /api/user/self", "PUT /api/user/setting",
		"GET /api/user/sessions", "DELETE /api/user/sessions/:sid", "POST /api/user/sessions/revoke-others",
		"GET /api/user/passkey", "POST /api/user/passkey/register/begin",
		"POST /api/user/passkey/register/finish", "DELETE /api/user/passkey",
		"GET /api/user/oauth/bindings", "DELETE /api/user/oauth/bindings/:provider_id",
		"POST /api/oauth/wechat/bind":
		c.Set(resellerSelfAccount, true)
		return true
	}
	return false
}

// ResellerSelfProfileWriteAllowed keeps the shared endpoint limited to language
// preferences and the proof-protected password change for active agents.
func ResellerSelfProfileWriteAllowed(c *gin.Context, data map[string]any) bool {
	if !IsResellerSelfAccount(c) {
		return true
	}
	if _, ok := data["language"].(string); ok && len(data) == 1 {
		return true
	}
	password, _ := data["password"].(string)
	current, _ := data["original_password"].(string)
	return len(data) == 2 && password != "" && current != ""
}
