package middleware

import "github.com/gin-gonic/gin"

// ModelCatalogNoStore also covers responses returned by the catalog's access gate.
func ModelCatalogNoStore(c *gin.Context) {
	c.Header("Cache-Control", "private, no-store")
	c.Next()
}
