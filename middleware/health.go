package middleware

import (
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
)

// RegisterHealthCheck must run before the business middleware is installed.
// The listener starts only after initialization; this probe checks that the
// gateway can serve HTTP without depending on logging or configuration locks.
func RegisterHealthCheck(server *gin.Engine) {
	server.GET("/healthz", func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.JSON(http.StatusOK, gin.H{
			"status":  "ready",
			"version": common.Version,
		})
	})
}
