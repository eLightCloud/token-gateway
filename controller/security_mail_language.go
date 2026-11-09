package controller

import (
	"github.com/QuantumNous/new-api/i18n"
	"github.com/gin-gonic/gin"
)

func securityMailLanguage(c *gin.Context) string {
	// The interface selection can differ from the last saved profile setting.
	if lang := c.GetHeader("Accept-Language"); lang != "" {
		return i18n.ParseAcceptLanguage(lang)
	}
	return i18n.GetLangFromContext(c)
}
