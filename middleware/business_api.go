package middleware

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/businessapi"
	"github.com/gin-gonic/gin"
)

// BusinessCaller strips private attribution before distribution, parsing, or raw
// passthrough. Replacing both body caches also protects internal retry paths.
func BusinessCaller() gin.HandlerFunc {
	return func(c *gin.Context) {
		source, err := businessapi.SourceForToken(c.GetInt("token_id"))
		if err != nil {
			c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"type": "server_error", "code": "caller_source_unavailable", "message": "Caller source configuration is unavailable"}})
			return
		}
		storage, err := common.GetBodyStorage(c)
		if err != nil {
			status := http.StatusBadRequest
			if common.IsRequestBodyTooLargeError(err) {
				status = http.StatusRequestEntityTooLarge
			}
			c.AbortWithStatusJSON(status, gin.H{"error": gin.H{"type": "invalid_request_error", "code": "invalid_caller_context", "param": "caller_context", "message": "Unable to read request body"}})
			return
		}
		body, err := storage.Bytes()
		if err != nil {
			c.AbortWithStatus(http.StatusBadRequest)
			return
		}
		var fields map[string]json.RawMessage
		if common.Unmarshal(body, &fields) != nil || fields == nil {
			// Leave legacy body validation and error shape to the existing relay.
			c.Next()
			return
		}
		caller := businessapi.Caller{SourceSystem: source}
		raw, supplied := fields["caller_context"]
		if supplied && string(bytes.TrimSpace(raw)) != "null" {
			var values map[string]json.RawMessage
			if common.Unmarshal(raw, &values) != nil || values == nil {
				abortCallerField(c, "caller_context", "caller_context must be an object")
				return
			}
			for key, value := range values {
				var target *string
				limit := 128
				switch key {
				case "source_system":
					target = &caller.SourceSystem
				case "tenant_id":
					target = &caller.TenantID
				case "tenant_name":
					target, limit = &caller.TenantName, 200
				case "caller_id":
					target = &caller.CallerID
				case "caller_name":
					target, limit = &caller.CallerName, 200
				default:
					abortCallerField(c, "caller_context."+key, "Unknown caller_context field")
					return
				}
				if string(bytes.TrimSpace(value)) == "null" {
					continue
				}
				var text string
				if common.Unmarshal(value, &text) != nil || !businessapi.ValidText(text, limit) {
					abortCallerField(c, "caller_context."+key, "Caller field must be a string within the allowed length and contain no control characters")
					return
				}
				text = strings.TrimSpace(text)
				if key == "source_system" {
					if text != "" && (source == "" || text != source) {
						abortCallerField(c, "caller_context.source_system", "source_system does not match the registered API key source")
						return
					}
					continue
				}
				*target = text
			}
		}
		if caller != (businessapi.Caller{}) {
			c.Set(businessapi.CallerKey, caller)
		}
		if supplied {
			delete(fields, "caller_context")
			cleanBody, marshalErr := common.Marshal(fields)
			if marshalErr != nil {
				c.AbortWithStatus(http.StatusBadRequest)
				return
			}
			cleanStorage, storageErr := common.CreateBodyStorage(cleanBody)
			if storageErr != nil {
				c.AbortWithStatus(http.StatusInternalServerError)
				return
			}
			_ = storage.Close()
			c.Set(common.KeyBodyStorage, cleanStorage)
			c.Set(common.KeyRequestBody, nil)
			c.Request.Body = io.NopCloser(bytes.NewReader(cleanBody))
			c.Request.ContentLength = int64(len(cleanBody))
			c.Request.GetBody = cleanStorage.NewReader
			c.Request.Header.Del("Content-Length")
		}
		c.Next()
	}
}

func abortCallerField(c *gin.Context, field, message string) {
	c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request_error", "code": "invalid_caller_context", "param": field, "message": message}})
}
