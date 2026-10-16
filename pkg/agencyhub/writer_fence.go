package agencyhub

import (
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func writerFenceReadOnly(method, path string) bool {
	if method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions {
		return true
	}
	for _, suffix := range []string{"/auth/logout", "/auth/change-password", "/auth/verify", "/auth/platform-link", "/auth/platform-link/remove"} {
		if strings.HasSuffix(path, suffix) {
			return true
		}
	}
	return false
}

// writerFenceMiddleware is the server-side old-writer barrier. Internal,
// signed TokenOps commands use CommandRouter and are intentionally unaffected.
func (a *App) writerFenceMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if writerFenceReadOnly(c.Request.Method, c.Request.URL.Path) || a.db == nil || !a.db.Migrator().HasTable(&model.AgencyOpsWriterState{}) {
			c.Next()
			return
		}
		var state model.AgencyOpsWriterState
		err := a.db.Select("mode", "fence_version").First(&state, 1).Error
		if err != nil && err != gorm.ErrRecordNotFound {
			respondError(c, http.StatusServiceUnavailable, "writer_fence_unavailable", "writer fence is unavailable", nil)
			return
		}
		if err == nil && (state.Mode == model.AgencyWriterDraining || state.Mode == model.AgencyWriterOpsSingle) {
			respondError(c, http.StatusConflict, "writer_moved_to_tokenops", "agency changes are controlled by TokenOps", gin.H{"writer_mode": state.Mode, "fence_version": state.FenceVersion})
			return
		}
		c.Next()
	}
}
