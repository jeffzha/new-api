/*
Copyright (C) 2023-2026 QuantumNous

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU Affero General Public License as
published by the Free Software Foundation, either version 3 of the
License, or (at your option) any later version.

This program is distributed in the hope that it will be useful,
but WITHOUT ANY WARRANTY; without even the implied warranty of
MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE. See the
GNU Affero General Public License for more details.

You should have received a copy of the GNU Affero General Public License
along with this program. If not, see <https://www.gnu.org/licenses/>.

For commercial licensing, please contact support@quantumnous.com
*/
package controller

import (
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func mcpEndpoint() string {
	baseURL, err := url.Parse(strings.TrimSpace(system_setting.ServerAddress))
	if err != nil || baseURL.Scheme == "" || baseURL.Host == "" {
		return "/mcp"
	}
	baseURL.Path = strings.TrimRight(baseURL.Path, "/") + "/mcp"
	baseURL.RawQuery = ""
	baseURL.Fragment = ""
	return baseURL.String()
}

type createMCPAccessCredentialRequest struct {
	Name      string `json:"name"`
	ExpiresAt *int64 `json:"expires_at,omitempty"`
	AllowIPs  string `json:"allow_ips,omitempty"`
}

func ListMCPAccessCredentials(c *gin.Context) {
	credentials, err := model.ListMCPAccessCredentials(c.GetInt(`id`))
	if err != nil {
		common.ApiError(c, err)
		return
	}
	summaries := make([]model.MCPAccessCredentialSummary, 0, len(credentials))
	for _, credential := range credentials {
		summaries = append(summaries, credential.Summary())
	}
	common.ApiSuccess(c, summaries)
}

func CreateMCPAccessCredential(c *gin.Context) {
	if middleware.RequireSecurityProof(c, service.VerificationOperation{Scope: service.VerificationScopeMCPAccessManage}) == nil {
		return
	}
	var request createMCPAccessCredentialRequest
	if err := common.DecodeJsonStrict(c.Request.Body, &request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{`success`: false, `message`: `请求格式错误`})
		return
	}
	credential, secret, err := model.CreateMCPAccessCredential(c.GetInt(`id`), request.Name, request.ExpiresAt, request.AllowIPs)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{`success`: false, `message`: err.Error()})
		return
	}
	model.RecordAuditLog(c, model.AuditLog{UserId: c.GetInt(`id`), ActorRole: c.GetInt(`role`), Category: model.AuditCategorySecurity, Action: `mcp_credential.create`, Content: `Created dedicated MCP pricing credential`, TokenRef: credential.TokenHash, Success: true, AuthMethod: `session`})
	c.JSON(http.StatusOK, gin.H{`success`: true, `message`: ``, `data`: gin.H{
		`credential`: credential.Summary(),
		`token`:      secret,
		`endpoint`:   mcpEndpoint(),
		`warning`:    `请立即保存此凭据；关闭后将无法再次查看明文。`,
	}})
}

func RotateMCPAccessCredential(c *gin.Context) {
	if middleware.RequireSecurityProof(c, service.VerificationOperation{Scope: service.VerificationScopeMCPAccessManage}) == nil {
		return
	}
	id, err := strconv.ParseInt(strings.TrimSpace(c.Param(`id`)), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{`success`: false, `message`: `MCP 凭据编号无效`})
		return
	}
	credential, secret, err := model.RotateMCPAccessCredential(c.GetInt(`id`), id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, gin.H{`success`: false, `message`: `未找到 MCP 凭据`})
		return
	}
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{`success`: false, `message`: `此 MCP 凭据已失效，无法轮换`})
		return
	}
	model.RecordAuditLog(c, model.AuditLog{UserId: c.GetInt(`id`), ActorRole: c.GetInt(`role`), Category: model.AuditCategorySecurity, Action: `mcp_credential.rotate`, Content: `Rotated dedicated MCP pricing credential`, TokenRef: credential.TokenHash, Success: true, AuthMethod: `session`})
	c.JSON(http.StatusOK, gin.H{`success`: true, `message`: ``, `data`: gin.H{
		`credential`: credential.Summary(),
		`token`:      secret,
		`endpoint`:   mcpEndpoint(),
		`warning`:    `请立即保存此凭据；原凭据已失效，关闭后将无法再次查看明文。`,
	}})
}

func RevokeMCPAccessCredential(c *gin.Context) {
	if middleware.RequireSecurityProof(c, service.VerificationOperation{Scope: service.VerificationScopeMCPAccessManage}) == nil {
		return
	}
	id, err := strconv.ParseInt(strings.TrimSpace(c.Param(`id`)), 10, 64)
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{`success`: false, `message`: `MCP 凭据编号无效`})
		return
	}
	credential, err := model.RevokeMCPAccessCredential(c.GetInt(`id`), id)
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, gin.H{`success`: false, `message`: `未找到 MCP 凭据`})
		return
	}
	if err != nil {
		common.ApiError(c, err)
		return
	}
	model.RecordAuditLog(c, model.AuditLog{UserId: c.GetInt(`id`), ActorRole: c.GetInt(`role`), Category: model.AuditCategorySecurity, Action: `mcp_credential.revoke`, Content: `Revoked dedicated MCP pricing credential`, TokenRef: credential.TokenHash, Success: true, AuthMethod: `session`})
	common.ApiSuccess(c, credential.Summary())
}
