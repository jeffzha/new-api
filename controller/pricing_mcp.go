package controller

import (
	"bytes"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencyhub"
	"github.com/gin-gonic/gin"
)

const platformPricingMCPTool = `list_platform_model_pricing`

type mcpRequest struct {
	JSONRPC string            `json:"jsonrpc"`
	ID      common.RawMessage `json:"id,omitempty"`
	Method  string            `json:"method"`
	Params  common.RawMessage `json:"params,omitempty"`
}

type mcpToolCallParams struct {
	Name      string            `json:"name"`
	Arguments common.RawMessage `json:"arguments,omitempty"`
}

type platformPricingQuery struct {
	ModelName   string `json:"model_name"`
	ChannelID   *int   `json:"channel_id,omitempty"`
	EnabledOnly *bool  `json:"enabled_only,omitempty"`
}

type platformPricingMCPRow struct {
	ModelName               string   `json:"model_name"`
	ChannelID               int      `json:"channel_id"`
	ChannelName             string   `json:"channel_name"`
	ChannelAvailable        bool     `json:"channel_available"`
	PlatformCostCoefficient *float64 `json:"platform_cost_coefficient,omitempty"`
	AgencyCostCoefficient   *float64 `json:"agency_cost_coefficient,omitempty"`
	DefaultSalesCoefficient *float64 `json:"default_sales_coefficient,omitempty"`
	PolicyRevision          int64    `json:"policy_revision"`
	UpdatedAtMS             int64    `json:"updated_at_ms"`
}

func PlatformPricingMCP(c *gin.Context) {
	var request mcpRequest
	if err := common.DecodeJsonStrict(c.Request.Body, &request); err != nil || request.JSONRPC != `2.0` || strings.TrimSpace(request.Method) == `` {
		writeMCPError(c, http.StatusBadRequest, request.ID, -32600, `Invalid JSON-RPC request`)
		return
	}

	switch request.Method {
	case `initialize`:
		writeMCPResult(c, request.ID, gin.H{
			`protocolVersion`: `2025-06-18`,
			`capabilities`:    gin.H{`tools`: gin.H{}},
			`serverInfo`:      gin.H{`name`: `platform-pricing`, `version`: common.Version},
		})
	case `notifications/initialized`:
		c.Status(http.StatusAccepted)
	case `tools/list`:
		writeMCPResult(c, request.ID, gin.H{`tools`: []gin.H{platformPricingToolDefinition()}})
	case `tools/call`:
		handlePlatformPricingMCPToolCall(c, request)
	default:
		writeMCPError(c, http.StatusBadRequest, request.ID, -32601, `Method not found`)
	}
}

func platformPricingToolDefinition() gin.H {
	return gin.H{
		`name`:        platformPricingMCPTool,
		`title`:       `Platform model pricing`,
		`description`: `Returns live platform model and channel cost coefficients plus model-level agency cost and default sales coefficients. It never returns channel credentials, upstream URLs, or procurement prices.`,
		`inputSchema`: gin.H{
			`type`: `object`,
			`properties`: gin.H{
				`model_name`:   gin.H{`type`: `string`, `description`: `Optional exact public model name.`},
				`channel_id`:   gin.H{`type`: `integer`, `minimum`: 1, `description`: `Optional channel ID.`},
				`enabled_only`: gin.H{`type`: `boolean`, `default`: true, `description`: `Whether to return only currently available channels.`},
			},
			`additionalProperties`: false,
		},
	}
}

func handlePlatformPricingMCPToolCall(c *gin.Context, request mcpRequest) {
	var params mcpToolCallParams
	if err := common.DecodeJsonStrict(bytes.NewReader(request.Params), &params); err != nil || params.Name != platformPricingMCPTool {
		writeMCPError(c, http.StatusBadRequest, request.ID, -32602, `Invalid tool arguments`)
		return
	}
	query, err := decodePlatformPricingQuery(params.Arguments)
	if err != nil {
		writeMCPError(c, http.StatusBadRequest, request.ID, -32602, err.Error())
		return
	}
	rows, revision, refreshedAtMS, err := queryPlatformPricing(query)
	if err != nil {
		common.SysError(`load MCP platform pricing: ` + err.Error())
		writeMCPError(c, http.StatusInternalServerError, request.ID, -32603, `Unable to load platform pricing`)
		return
	}
	structured := gin.H{`policy_revision`: revision, `updated_at_ms`: refreshedAtMS, `items`: rows}
	encoded, err := common.Marshal(structured)
	if err != nil {
		writeMCPError(c, http.StatusInternalServerError, request.ID, -32603, `Unable to encode platform pricing`)
		return
	}
	writeMCPResult(c, request.ID, gin.H{
		`content`:           []gin.H{{`type`: `text`, `text`: string(encoded)}},
		`structuredContent`: structured,
	})
}

func decodePlatformPricingQuery(raw common.RawMessage) (platformPricingQuery, error) {
	query := platformPricingQuery{}
	if len(raw) == 0 || string(raw) == `null` {
		return query, nil
	}
	if err := common.DecodeJsonStrict(bytes.NewReader(raw), &query); err != nil {
		return platformPricingQuery{}, fmt.Errorf(`arguments must be a JSON object`)
	}
	query.ModelName = strings.TrimSpace(query.ModelName)
	if len([]rune(query.ModelName)) > 191 {
		return platformPricingQuery{}, fmt.Errorf(`model_name is too long`)
	}
	if query.ChannelID != nil && *query.ChannelID <= 0 {
		return platformPricingQuery{}, fmt.Errorf(`channel_id must be positive`)
	}
	return query, nil
}

func queryPlatformPricing(query platformPricingQuery) ([]platformPricingMCPRow, int64, int64, error) {
	catalog, err := agencyhub.LoadPlatformPricingCatalog(model.DB)
	if err != nil {
		return nil, 0, 0, err
	}
	enabledOnly := query.EnabledOnly == nil || *query.EnabledOnly
	items := make([]platformPricingMCPRow, 0)
	for _, modelRow := range catalog.Items {
		if query.ModelName != `` && modelRow.OriginModelName != query.ModelName {
			continue
		}
		for _, channel := range modelRow.ChannelCosts {
			if query.ChannelID != nil && channel.ChannelID != *query.ChannelID {
				continue
			}
			if enabledOnly && !channel.Available {
				continue
			}
			items = append(items, platformPricingMCPRow{
				ModelName:               modelRow.OriginModelName,
				ChannelID:               channel.ChannelID,
				ChannelName:             channel.ChannelName,
				ChannelAvailable:        channel.Available,
				PlatformCostCoefficient: bpsCoefficient(channel.PlatformCostBPS),
				AgencyCostCoefficient:   bpsCoefficient(modelRow.AgencyCostBPS),
				DefaultSalesCoefficient: bpsCoefficient(modelRow.DefaultSalesBPS),
				PolicyRevision:          catalog.Revision,
				UpdatedAtMS:             catalog.RefreshedAtMS,
			})
		}
	}
	return items, catalog.Revision, catalog.RefreshedAtMS, nil
}

func bpsCoefficient(bps *int) *float64 {
	if bps == nil {
		return nil
	}
	value := float64(*bps) / 10000
	return &value
}

func writeMCPResult(c *gin.Context, id common.RawMessage, result any) {
	c.Header(`Cache-Control`, `no-store`)
	c.JSON(http.StatusOK, gin.H{`jsonrpc`: `2.0`, `id`: id, `result`: result})
}

func writeMCPError(c *gin.Context, status int, id common.RawMessage, code int, message string) {
	c.Header(`Cache-Control`, `no-store`)
	c.JSON(status, gin.H{`jsonrpc`: `2.0`, `id`: id, `error`: gin.H{`code`: code, `message`: message}})
}

type playgroundPricingAssistantRequest struct {
	Message string `json:"message"`
}

func PlaygroundPricingAssistant(c *gin.Context) {
	var request playgroundPricingAssistantRequest
	if err := common.DecodeJsonStrict(c.Request.Body, &request); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{`success`: false, `message`: "\u8bf7\u6c42\u683c\u5f0f\u9519\u8bef"})
		return
	}
	message := strings.TrimSpace(request.Message)
	if message == `` || len([]rune(message)) > 1000 {
		c.JSON(http.StatusBadRequest, gin.H{`success`: false, `message`: "\u8bf7\u8f93\u5165\u4e0d\u8d85\u8fc7 1000 \u4e2a\u5b57\u7b26\u7684\u95ee\u9898"})
		return
	}
	query, err := playgroundPricingQuery(message)
	if err != nil {
		common.SysError(`load playground pricing assistant: ` + err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{`success`: false, `message`: "\u6682\u65f6\u65e0\u6cd5\u8bfb\u53d6\u5e73\u53f0\u4ef7\u683c\u7b56\u7565"})
		return
	}
	rows, revision, refreshedAtMS, err := queryPlatformPricing(query)
	if err != nil {
		common.SysError(`query playground pricing assistant: ` + err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{`success`: false, `message`: "\u6682\u65f6\u65e0\u6cd5\u8bfb\u53d6\u5e73\u53f0\u4ef7\u683c\u7b56\u7565"})
		return
	}
	if query.ModelName == `` && query.ChannelID == nil && len(rows) > 80 {
		rows = rows[:80]
	}
	c.JSON(http.StatusOK, gin.H{`success`: true, `data`: gin.H{
		`message`:         formatPricingAssistantReply(query, rows, revision),
		`items`:           rows,
		`policy_revision`: revision,
		`updated_at_ms`:   refreshedAtMS,
	}})
}

func playgroundPricingQuery(message string) (platformPricingQuery, error) {
	catalog, err := agencyhub.LoadPlatformPricingCatalog(model.DB)
	if err != nil {
		return platformPricingQuery{}, err
	}
	query := platformPricingQuery{}
	normalizedMessage := strings.ToLower(message)
	for _, item := range catalog.Items {
		if strings.Contains(normalizedMessage, strings.ToLower(item.OriginModelName)) {
			query.ModelName = item.OriginModelName
			break
		}
	}
	if channelID := channelIDFromMessage(message); channelID > 0 {
		query.ChannelID = &channelID
	}
	return query, nil
}

// channelIDFromMessage intentionally requires an explicit channel marker.
// It must not treat model versions such as v4 as a channel ID.
func channelIDFromMessage(message string) int {
	normalized := strings.ToLower(strings.TrimSpace(message))
	position := strings.Index(normalized, `channel`)
	markerLength := len(`channel`)
	if position < 0 {
		position = strings.Index(normalized, "渠道")
		markerLength = len("渠道")
	}
	if position < 0 {
		return 0
	}
	remainder := strings.TrimSpace(normalized[position+markerLength:])
	remainder = strings.TrimPrefix(remainder, `id`)
	remainder = strings.TrimPrefix(remainder, "编号")
	remainder = strings.TrimLeft(strings.TrimSpace(remainder), `#:： `)
	if remainder == `` {
		return 0
	}
	end := 0
	for end < len(remainder) && remainder[end] >= '0' && remainder[end] <= '9' {
		end++
	}
	if end == 0 {
		return 0
	}
	channelID, err := strconv.Atoi(remainder[:end])
	if err != nil || channelID <= 0 {
		return 0
	}
	return channelID
}

func formatPricingAssistantReply(query platformPricingQuery, rows []platformPricingMCPRow, revision int64) string {
	if len(rows) == 0 {
		if query.ModelName != `` {
			return "\u672a\u627e\u5230\u6a21\u578b " + query.ModelName + " \u7684\u5f53\u524d\u53ef\u7528\u4ef7\u683c\u7b56\u7565\u3002\u8bf7\u786e\u8ba4\u6a21\u578b\u540d\u79f0\u6216\u5237\u65b0\u6e20\u9053\u914d\u7f6e\u540e\u91cd\u8bd5\u3002"
		}
		return "\u5f53\u524d\u6ca1\u6709\u53ef\u5c55\u793a\u7684\u6a21\u578b\u6e20\u9053\u4ef7\u683c\u7b56\u7565\u3002"
	}
	var builder strings.Builder
	builder.WriteString("### \u5e73\u53f0\u4ef7\u683c\u7b56\u7565\n\n")
	builder.WriteString("\u5f53\u524d\u7b56\u7565\u7248\u672c\uff1a")
	builder.WriteString(strconv.FormatInt(revision, 10))
	builder.WriteString("\u3002\u5e73\u53f0\u6210\u672c\u6309\u6a21\u578b + \u6e20\u9053\u7ef4\u62a4\uff1b\u4ee3\u7406\u5546\u6210\u672c\u548c\u9ed8\u8ba4\u9500\u552e\u7cfb\u6570\u6309\u6a21\u578b\u7ef4\u62a4\u3002\n\n")
	builder.WriteString("| \u6a21\u578b | \u6e20\u9053 | \u5e73\u53f0\u6210\u672c\u7cfb\u6570 | \u4ee3\u7406\u5546\u6210\u672c\u7cfb\u6570 | \u9ed8\u8ba4\u9500\u552e\u7cfb\u6570 |\n| --- | --- | ---: | ---: | ---: |\n")
	for _, row := range rows {
		builder.WriteString(`| `)
		builder.WriteString(row.ModelName)
		builder.WriteString(" | " + row.ChannelName + " (#" + strconv.Itoa(row.ChannelID) + ") | " + formatCoefficient(row.PlatformCostCoefficient) + " | " + formatCoefficient(row.AgencyCostCoefficient) + " | " + formatCoefficient(row.DefaultSalesCoefficient) + " |\n")
	}
	return builder.String()
}

func formatCoefficient(value *float64) string {
	if value == nil {
		return "\u672a\u8bbe\u7f6e"
	}
	return strconv.FormatFloat(*value, 'f', -1, 64)
}
