package model

import (
	"errors"

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

// Scope at the database boundary; another key on the same account cannot read
// a business system's invocation by knowing its request ID.
func GetBusinessRequestLog(userID, tokenID int, requestID string) (*Log, error) {
	if userID <= 0 || tokenID <= 0 || requestID == "" {
		return nil, errors.New("request identity is required")
	}
	if LOG_DB == nil {
		return nil, errors.New("log database is not initialized")
	}
	var log Log
	err := LOG_DB.Where("user_id = ? AND token_id = ? AND request_id = ? AND type IN ?", userID, tokenID, requestID, []int{LogTypeConsume, LogTypeError}).
		Order("type ASC").Order("created_at DESC").Take(&log).Error
	return &log, err
}
