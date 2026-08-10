package router

import (
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/gin-gonic/gin"
)

func registerWorkbenchIdentityRoutes(apiRouter *gin.RouterGroup) {
	workbench := apiRouter.Group("/workbench")
	workbench.POST("/session-ticket", middleware.UserAuth(), middleware.CriticalRateLimit(), controller.IssueWorkbenchSessionTicket)

	admin := apiRouter.Group("/admin/workbench")
	admin.POST("/session-ticket", middleware.RootAuth(), middleware.CriticalRateLimit(), controller.IssueWorkbenchAdminSessionTicket)

	internal := apiRouter.Group("/internal/workbench")
	internal.POST("/identity-status", controller.GetWorkbenchIdentityStatus)
	internal.POST("/admin-identity-status", controller.GetWorkbenchAdminIdentityStatus)
}
