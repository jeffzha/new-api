package controller

import (
	"context"
	"fmt"
	"net/http"
	"strconv"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/authz"
	"github.com/QuantumNous/new-api/service/modelhealth"
	"github.com/gin-gonic/gin"
)

func modelHealthNumber(c *gin.Context, key string) int64 {
	n, _ := strconv.ParseInt(c.Query(key), 10, 64)
	return n
}
func modelHealthSelection(c *gin.Context) modelhealth.Selection {
	return modelhealth.Selection{Channel: int(modelHealthNumber(c, "channel_id")), Model: c.Query("model"), Endpoint: c.Query("endpoint")}
}

func modelHealthReply(c *gin.Context, data any, err error) {
	if err == nil {
		common.ApiSuccess(c, data)
		return
	}
	if modelhealth.IsValidation(err) {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	c.JSON(http.StatusInternalServerError, gin.H{"success": false, "message": "Model health operation failed"})
}

func healthAudit(c *gin.Context, action string, err error) {
	model.RecordAuditLog(c, model.AuditLog{UserId: c.GetInt("id"), ActorRole: c.GetInt("role"), Category: model.AuditCategoryOperation, Action: "model_health." + action, Success: err == nil})
	markAuditLogged(c)
}

func healthMutation[T any](action string, apply func(*gin.Context, T) (any, error)) gin.HandlerFunc {
	return func(c *gin.Context) {
		var input T
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 32<<10)
		if err := c.ShouldBindJSON(&input); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid model health configuration"})
			return
		}
		data, err := apply(c, input)
		modelHealthReply(c, data, err)
		healthAudit(c, action, err)
	}
}

func RegisterModelHealthRoutes(r *gin.RouterGroup, guard func(authz.Permission) func(*gin.Context)) {
	reads := r.Group("", middleware.ModelHealthReadRateLimit())
	reads.GET("/overview", func(c *gin.Context) {
		result, err := modelhealth.List(c.Request.Context(), modelhealth.Filter{Search: c.Query("q"), Channel: c.Query("channel"), Kind: c.Query("type"), Group: c.Query("group"), Endpoint: c.Query("endpoint"), State: c.Query("state"), From: modelHealthNumber(c, "from"), To: modelHealthNumber(c, "to"), Page: int(modelHealthNumber(c, "page")), Limit: 20})
		modelHealthReply(c, result, err)
	})
	reads.GET("/capabilities", func(c *gin.Context) {
		id, role := c.GetInt("id"), c.GetInt("role")
		common.ApiSuccess(c, gin.H{"probe": authz.Can(id, role, authz.ModelHealthProbe), "rules": authz.Can(id, role, authz.ModelHealthRules), "notify": authz.Can(id, role, authz.ModelHealthNotify), "export": authz.Can(id, role, authz.ModelHealthExport), "worker": modelhealth.WorkerAvailable(c.Request.Context())})
	})
	planHandler := func(c *gin.Context) {
		data, err := modelhealth.GetPlan(c.Request.Context(), modelHealthSelection(c))
		modelHealthReply(c, data, err)
	}
	reads.GET("/plan", guard(authz.ModelHealthProbe), planHandler)
	reads.GET("/template", guard(authz.ModelHealthRules), planHandler)
	reads.GET("/auto-templates", guard(authz.ModelHealthRules), func(c *gin.Context) {
		p, err := modelhealth.GetAutoTemplatePolicy(c.Request.Context())
		modelHealthReply(c, p, err)
	})
	reads.GET("/runs", func(c *gin.Context) {
		data, err := modelhealth.ListRuns(c.Request.Context(), c.Query("source"), int(modelHealthNumber(c, "page")))
		modelHealthReply(c, data, err)
	})
	reads.GET("/run-costs", func(c *gin.Context) {
		data, err := modelhealth.GetOpsCosts(c.Request.Context(), c.Query("source"), int(modelHealthNumber(c, "page")))
		modelHealthReply(c, data, err)
	})
	reads.GET("/budget", guard(authz.ModelHealthRules), func(c *gin.Context) {
		data, err := modelhealth.GetBudget(c.Request.Context())
		modelHealthReply(c, data, err)
	})
	reads.GET("/rule", guard(authz.ModelHealthRules), func(c *gin.Context) {
		data, err := modelhealth.GetRule(c.Request.Context(), c.Query("scope"))
		modelHealthReply(c, data, err)
	})
	reads.GET("/notifications", guard(authz.ModelHealthNotify), func(c *gin.Context) {
		data, err := modelhealth.ListDestinations(c.Request.Context(), int(modelHealthNumber(c, "page")))
		modelHealthReply(c, data, err)
	})
	reads.GET("/deliveries", guard(authz.ModelHealthNotify), func(c *gin.Context) {
		data, err := modelhealth.ListDeliveries(c.Request.Context(), int(modelHealthNumber(c, "page")))
		modelHealthReply(c, data, err)
	})
	exportEvidence := func(fetch func(context.Context, int64) (string, error)) gin.HandlerFunc {
		return func(c *gin.Context) {
			id, _ := strconv.ParseInt(c.Param("id"), 10, 64)
			if id < 1 {
				c.Status(http.StatusNotFound)
				return
			}
			text, err := fetch(c.Request.Context(), id)
			if err != nil {
				modelHealthReply(c, nil, err)
			} else {
				c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=model-health-%d.md", id))
				c.Data(http.StatusOK, "text/markdown; charset=utf-8", []byte(text))
			}
			healthAudit(c, "export", err)
		}
	}
	actions := r.Group("", middleware.ModelHealthActionRateLimit())
	actions.GET("/evidence/:id", guard(authz.ModelHealthExport), exportEvidence(modelhealth.Evidence))
	actions.GET("/user-evidence/:id", guard(authz.ModelHealthExport), exportEvidence(modelhealth.UserEvidence))
	type probeInput struct {
		modelhealth.Selection
		Revision string `json:"revision"`
		Ceiling  int64  `json:"max_micro"`
	}
	actions.POST("/probe", guard(authz.ModelHealthProbe), healthMutation("probe", func(c *gin.Context, p probeInput) (any, error) {
		return modelhealth.Enqueue(c.Request.Context(), p.Selection, p.Revision, p.Ceiling)
	}))
	actions.PUT("/template", guard(authz.ModelHealthRules), healthMutation("template", func(c *gin.Context, p modelhealth.Template) (any, error) {
		return nil, modelhealth.SaveTemplate(c.Request.Context(), p)
	}))
	actions.PUT("/auto-templates", guard(authz.ModelHealthRules), healthMutation("auto_templates", func(c *gin.Context, p modelhealth.AutoTemplatePolicy) (any, error) {
		return nil, modelhealth.SaveAutoTemplatePolicy(c.Request.Context(), p)
	}))
	type ruleInput struct {
		modelhealth.Selection
		Scope string           `json:"scope"`
		Rule  modelhealth.Rule `json:"rule"`
	}
	actions.PUT("/rule", guard(authz.ModelHealthRules), healthMutation("rule", func(c *gin.Context, p ruleInput) (any, error) {
		return nil, modelhealth.SaveRule(c.Request.Context(), p.Scope, p.Selection, p.Rule)
	}))
	actions.PUT("/budget", guard(authz.ModelHealthRules), healthMutation("budget", func(c *gin.Context, p modelhealth.Budget) (any, error) {
		return nil, modelhealth.SaveBudget(c.Request.Context(), p)
	}))
	actions.PUT("/notifications", guard(authz.ModelHealthNotify), healthMutation("notification", func(c *gin.Context, p modelhealth.Destination) (any, error) {
		return nil, modelhealth.SaveDestination(c.Request.Context(), p)
	}))
	type testInput struct {
		ID string `json:"id"`
	}
	actions.POST("/notifications/test", guard(authz.ModelHealthNotify), healthMutation("notification_test", func(c *gin.Context, p testInput) (any, error) {
		return nil, modelhealth.TestDestination(c.Request.Context(), p.ID)
	}))
	type reconcileInput struct {
		ID     int64  `json:"id"`
		Actual *int64 `json:"actual_micro" binding:"required,gte=0"`
	}
	actions.POST("/reconcile", guard(authz.ModelHealthRules), healthMutation("reconcile", func(c *gin.Context, p reconcileInput) (any, error) {
		return nil, modelhealth.Reconcile(c.Request.Context(), p.ID, *p.Actual)
	}))
}
