package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUpdateOptionRejectsUnsafeModelCompatibilityRegistry(t *testing.T) {
	response := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(response)
	context.Request = httptest.NewRequest(
		http.MethodPut,
		"/api/option/",
		strings.NewReader(`{"key":"model_compatibility.registry","value":"{\"schema_version\":1,\"enabled\":true,\"active_version\":\"v1\",\"versions\":[{\"id\":\"v1\",\"profiles\":[{\"id\":\"unsafe\",\"channel_types\":[1],\"model_regex\":\".*\",\"relay_formats\":[\"openai\"],\"rules\":[{\"path\":\"messages.0.content\",\"action\":\"drop\"}]}]}]}"}`),
	)

	UpdateOption(context)

	assert.Equal(t, http.StatusOK, response.Code)
	var payload struct {
		Success bool   `json:"success"`
		Message string `json:"message"`
	}
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &payload))
	assert.False(t, payload.Success)
	assert.Contains(t, payload.Message, "protected")
}
