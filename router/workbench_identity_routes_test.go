package router

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestWorkbenchIdentityRoutesExposeOnlyTheMinimalBridgeEndpoints(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	registerWorkbenchIdentityRoutes(router.Group("/api"))

	routes := router.Routes()
	registered := map[string]string{}
	for _, route := range routes {
		registered[route.Path] = route.Method
	}
	assert.Equal(t, http.MethodPost, registered["/api/workbench/session-ticket"])
	assert.Equal(t, http.MethodPost, registered["/api/admin/workbench/session-ticket"])
	assert.Equal(t, http.MethodPost, registered["/api/internal/workbench/identity-status"])
	assert.Equal(t, http.MethodPost, registered["/api/internal/workbench/admin-identity-status"])
	assert.Len(t, routes, 4)
}
