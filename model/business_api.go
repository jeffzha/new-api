package model

import (
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/businessapi"
	"github.com/gin-gonic/gin"
)

// Caller attribution is stored with the invocation, independently of the
// account used for permissions and billing. It is never part of UI projections.
const logOtherCallerContextKey = "caller_context"

func businessCallerLogJSON(c *gin.Context, other *LogOther) string {
	caller, exists := businessapi.FromContext(c)
	if !exists {
		return other.JSONString()
	}
	values := other.toMap()
	values[logOtherCallerContextKey] = caller
	data, err := common.Marshal(values)
	if err != nil {
		common.SysError("failed to marshal business invocation metadata")
		return other.JSONString()
	}
	return string(data)
}
