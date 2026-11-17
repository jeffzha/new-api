package controller

import (
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencyhub"
	"github.com/gin-gonic/gin"
)

// RegisterCreditBookRoutes exposes the super administrator credit book on the
// main site API. Callers must already have authenticated the request as root:
// the handlers never widen access on their own, and the Agency Hub keeps the
// background statement and email lanes that this book feeds.
func RegisterCreditBookRoutes(group *gin.RouterGroup) {
	app := agencyhub.New(model.DB, model.LOG_DB, agencyhub.LoadConfig())
	app.CreditBookRoutes(group)
}

// CreditBookRootIdentity hands the credit book the actor to record. The book
// lives in the Agency Hub package and reads its caller from that package's
// identity slot, so a main-site edit is attributed to the signed-in root user
// instead of being rejected as an anonymous writer.
func CreditBookRootIdentity() gin.HandlerFunc {
	return func(c *gin.Context) {
		if userID := c.GetInt("id"); userID > 0 {
			c.Set("agency_identity", agencyhub.PlatformRootIdentity(int64(userID)))
		}
		c.Next()
	}
}
