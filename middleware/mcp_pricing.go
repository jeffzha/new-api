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
package middleware

import (
	"errors"
	"net/http"

	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

const mcpCredentialContextKey = `mcp_access_credential`

// MCPPricingAuth accepts either a root dashboard credential for internal use
// or a purpose-built, least-privilege MCP credential for external consumers.
// Relay API keys and dashboard PATs are never accepted as external MCP keys.
func MCPPricingAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		raw, ok := authorizationToken(c.GetHeader(`Authorization`))
		if ok && len(raw) >= 4 && raw[:4] == `mcp_` {
			credential, err := model.AuthenticateMCPAccessCredential(raw, c.ClientIP())
			if err != nil {
				status := http.StatusUnauthorized
				if errors.Is(err, model.ErrMCPAccessCredentialIPNotAllowed) {
					status = http.StatusForbidden
				}
				c.AbortWithStatusJSON(status, gin.H{`success`: false, `code`: `MCP_CREDENTIAL_INVALID`, `message`: `MCP access credential is invalid or unavailable`})
				return
			}
			c.Set(mcpCredentialContextKey, *credential)
			c.Set("id", credential.OwnerUserID)
			c.Next()
			return
		}
		RootAuth()(c)
	}
}

func GetMCPAccessCredential(c *gin.Context) (model.MCPAccessCredential, bool) {
	value, ok := c.Get(mcpCredentialContextKey)
	if !ok {
		return model.MCPAccessCredential{}, false
	}
	credential, ok := value.(model.MCPAccessCredential)
	return credential, ok
}

// MCPPricingRateLimit provides a separate conservative budget for the public
// protocol endpoint, independent of the regular dashboard request budget.
func MCPPricingRateLimit() gin.HandlerFunc {
	return rateLimitFactory(60, 60, `MCP:pricing:`)
}
