package middleware

import (
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

// AuthorizeKeyedChatModel applies the current token model policy before an
// idempotency key is consulted or claimed. A receipt can be replayed without
// routing to an upstream channel.
func AuthorizeKeyedChatModel(c *gin.Context) bool {
	_, pinned, _ := service.GetChannelConstraints(c).ResolvedPin()
	if pinned || !common.GetContextKeyBool(c, constant.ContextKeyTokenModelLimitEnabled) {
		return true
	}
	request, _, err := getModelRequest(c)
	if err != nil {
		abortWithOpenAiMessage(c, invalidRequestStatus(err), i18n.T(c, i18n.MsgDistributorInvalidRequest, map[string]any{"Error": err.Error()}))
		return false
	}
	return authorizeTokenModel(c, request.Model)
}

func authorizeTokenModel(c *gin.Context, modelName string) bool {
	if !common.GetContextKeyBool(c, constant.ContextKeyTokenModelLimitEnabled) {
		return true
	}
	value, exists := common.GetContextKey(c, constant.ContextKeyTokenModelLimit)
	if !exists {
		abortWithOpenAiMessage(c, http.StatusForbidden, i18n.T(c, i18n.MsgDistributorTokenNoModelAccess))
		return false
	}
	limits, _ := value.(map[string]bool)
	if !TokenModelLimitAllows(limits, modelName) {
		abortWithOpenAiMessage(c, http.StatusForbidden, i18n.T(c, i18n.MsgDistributorTokenModelForbidden, map[string]any{"Model": modelName}))
		return false
	}
	return true
}
