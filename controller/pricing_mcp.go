package controller

import (
	"bytes"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/middleware"
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
	Name       string            `json:"name"`
	ToolName   string            `json:"tool_name,omitempty"`
	ToolNameV2 string            `json:"toolName,omitempty"`
	Arguments  common.RawMessage `json:"arguments,omitempty"`
	Input      common.RawMessage `json:"input,omitempty"`
	Meta       common.RawMessage `json:"_meta,omitempty"`
}

type platformPricingQuery struct {
	ModelName   string `json:"model_name"`
	ChannelID   *int   `json:"channel_id,omitempty"`
	EnabledOnly *bool  `json:"enabled_only,omitempty"`
}

type platformPricingMCPRow struct {
	ModelName               string   `json:"model_name"`
	ChannelID               int      `json:"channel_id,omitempty"`
	ChannelName             string   `json:"channel_name,omitempty"`
	ChannelAvailable        bool     `json:"channel_available"`
	PlatformCostCoefficient *float64 `json:"platform_cost_coefficient,omitempty"`
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
		writeMCPResult(c, request.ID, gin.H{`tools`: []gin.H{platformPricingToolDefinition(c)}})
	case `tools/call`:
		handlePlatformPricingMCPToolCall(c, request)
	default:
		writeMCPError(c, http.StatusBadRequest, request.ID, -32601, `Method not found`)
	}
}

func platformPricingToolDefinition(c *gin.Context) gin.H {
	description := "Returns live public model availability and platform cost coefficients by channel. It never returns agency or reseller prices, channel credentials, upstream URLs, or procurement prices."
	properties := gin.H{
		`model_name`:   gin.H{`type`: `string`, `description`: `Optional exact public model name.`},
		`channel_id`:   gin.H{`type`: `integer`, `minimum`: 1, `description`: `Optional channel ID.`},
		`enabled_only`: gin.H{`type`: `boolean`, `default`: true, `description`: `Whether to return only currently available channels.`},
	}
	if _, external := middleware.GetMCPAccessCredential(c); external {
		description = "Returns live public model availability and platform cost coefficients. It never returns channel identities, agency or reseller prices, channel credentials, upstream URLs, or procurement prices."
		properties = gin.H{`model_name`: properties[`model_name`], `enabled_only`: properties[`enabled_only`]}
	}
	return gin.H{
		`name`:        platformPricingMCPTool,
		`title`:       `Platform model pricing`,
		`description`: description,
		`inputSchema`: gin.H{
			`type`:                 `object`,
			`properties`:           properties,
			`additionalProperties`: false,
		},
	}
}

func handlePlatformPricingMCPToolCall(c *gin.Context, request mcpRequest) {
	params, err := decodeMCPToolCallParams(request.Params)
	if err != nil || normalizePlatformPricingMCPToolName(params.toolName()) != platformPricingMCPTool {
		writeMCPError(c, http.StatusBadRequest, request.ID, -32602, `Invalid tool arguments`)
		return
	}
	query, err := decodePlatformPricingQuery(params.arguments())
	if err != nil {
		writeMCPError(c, http.StatusBadRequest, request.ID, -32602, err.Error())
		return
	}
	if _, external := middleware.GetMCPAccessCredential(c); external && query.ChannelID != nil {
		writeMCPError(c, http.StatusBadRequest, request.ID, -32602, `External MCP credentials cannot filter by channel`)
		return
	}
	rows, revision, refreshedAtMS, err := queryPlatformPricing(query)
	if err != nil {
		common.SysError(`load MCP platform pricing: ` + err.Error())
		writeMCPError(c, http.StatusInternalServerError, request.ID, -32603, `Unable to load platform pricing`)
		return
	}
	if credential, external := middleware.GetMCPAccessCredential(c); external {
		rows = publicMCPPricingRows(rows)
		model.RecordAuditLog(c, model.AuditLog{
			UserId:     credential.OwnerUserID,
			ActorRole:  common.RoleRootUser,
			Category:   model.AuditCategorySecurity,
			Action:     "mcp_pricing.read",
			Content:    "External MCP pricing query",
			TokenRef:   credential.TokenHash,
			Success:    true,
			AuthMethod: "mcp_credential",
		})
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

func (params mcpToolCallParams) toolName() string {
	if params.Name != `` {
		return params.Name
	}
	if params.ToolName != `` {
		return params.ToolName
	}
	return params.ToolNameV2
}

func (params mcpToolCallParams) arguments() common.RawMessage {
	if len(params.Arguments) != 0 {
		return params.Arguments
	}
	return params.Input
}

// decodeMCPToolCallParams accepts the standard object form and the JSON-string
// envelope emitted by some MCP client bridges. The outer envelope is decoded
// permissively because MCP clients may attach transport metadata such as a
// progress token. The tool name remains allowlisted and arguments are decoded
// strictly below, so metadata cannot alter the tool's business input.
func decodeMCPToolCallParams(raw common.RawMessage) (mcpToolCallParams, error) {
	var params mcpToolCallParams
	if err := common.Unmarshal(raw, &params); err == nil {
		return params, nil
	}
	var encoded string
	if err := common.Unmarshal(raw, &encoded); err != nil {
		return mcpToolCallParams{}, err
	}
	if err := common.UnmarshalJsonStr(encoded, &params); err != nil {
		return mcpToolCallParams{}, err
	}
	return params, nil
}

// normalizePlatformPricingMCPToolName accepts the namespaced form emitted by
// MCP clients such as WorkBuddy while retaining a strict one-tool allowlist.
// Those clients prefix a remote tool with `mcp__<server name>__`; the protocol
// server itself advertises only the unqualified tool name.
func normalizePlatformPricingMCPToolName(name string) string {
	name = strings.TrimSpace(name)
	if name == platformPricingMCPTool {
		return name
	}
	canonical := strings.NewReplacer(
		` `, ``,
		`_`, ``,
		`-`, ``,
		`/`, ``,
		`:`, ``,
		`.`, ``,
	).Replace(strings.ToLower(name))
	if strings.HasPrefix(canonical, `mcp`) && strings.HasSuffix(canonical, `listplatformmodelpricing`) && canonical != `mcplistplatformmodelpricing` {
		return platformPricingMCPTool
	}
	return name
}

func publicMCPPricingRows(rows []platformPricingMCPRow) []platformPricingMCPRow {
	byModel := make(map[string]platformPricingMCPRow, len(rows))
	for _, row := range rows {
		existing, found := byModel[row.ModelName]
		if found {
			existing.ChannelAvailable = existing.ChannelAvailable || row.ChannelAvailable
			existing.PlatformCostCoefficient = lowerCoefficient(existing.PlatformCostCoefficient, row.PlatformCostCoefficient)
			byModel[row.ModelName] = existing
			continue
		}
		row.ChannelID = 0
		row.ChannelName = ""
		byModel[row.ModelName] = row
	}
	items := make([]platformPricingMCPRow, 0, len(byModel))
	for _, row := range rows {
		if result, found := byModel[row.ModelName]; found {
			items = append(items, result)
			delete(byModel, row.ModelName)
		}
	}
	return items
}

func lowerCoefficient(left, right *float64) *float64 {
	if left == nil {
		return right
	}
	if right == nil || *left <= *right {
		return left
	}
	return right
}

func decodePlatformPricingQuery(raw common.RawMessage) (platformPricingQuery, error) {
	query := platformPricingQuery{}
	if len(raw) == 0 || string(raw) == `null` {
		return query, nil
	}
	if err := common.DecodeJsonStrict(bytes.NewReader(raw), &query); err != nil {
		var encoded string
		if decodeStringErr := common.DecodeJsonStrict(bytes.NewReader(raw), &encoded); decodeStringErr != nil || common.DecodeJsonStrict(strings.NewReader(encoded), &query) != nil {
			return platformPricingQuery{}, fmt.Errorf(`arguments must be a JSON object`)
		}
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
	names := make([]string, 0, len(catalog.Items))
	for _, modelRow := range catalog.Items {
		names = append(names, modelRow.OriginModelName)
	}
	publicModels, err := model.PublicModelNames(model.DB, names)
	if err != nil {
		return nil, 0, 0, err
	}
	enabledOnly := query.EnabledOnly == nil || *query.EnabledOnly
	items := make([]platformPricingMCPRow, 0)
	for _, modelRow := range catalog.Items {
		if !publicModels[modelRow.OriginModelName] {
			continue
		}
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

type playgroundPricingAssistantIntent struct {
	Query          platformPricingQuery
	ListRequested  bool
	RulesRequested bool
}

func pricingAssistantLanguageModelContext(message string, intent playgroundPricingAssistantIntent, rows []platformPricingMCPRow, _ int64) string {
	// This handler is Root-only. The selected model receives the same
	// authorized public model and platform cost fields shown by MCP.
	encodedRows, err := common.Marshal(rows)
	if err != nil {
		return ""
	}
	return strings.Join([]string{
		"你是平台价格策略助手。下面的“已授权查询结果”由系统刚刚实时查询并已完成权限校验。",
		"必须根据这些结果直接、自然地回答用户的问题，可以比较、解释和总结其中的公开模型、渠道与平台成本系数；不得声称没有工具、没有数据或需要用户另行授权。",
		"不得猜测、杜撰或使用查询结果以外的价格和渠道信息；不得执行修改、发布、调用模型或其他操作。使用简体中文。",
		"用户问题：" + message,
		"已授权查询结果：" + string(encodedRows),
	}, "\n\n")
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
	intent, err := playgroundPricingIntentFromMessage(message)
	if err != nil {
		common.SysError(`load playground pricing assistant: ` + err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{`success`: false, `message`: "\u6682\u65f6\u65e0\u6cd5\u8bfb\u53d6\u5e73\u53f0\u4ef7\u683c\u7b56\u7565"})
		return
	}
	rows, revision, refreshedAtMS, err := queryPlatformPricing(intent.Query)
	if err != nil {
		common.SysError(`query playground pricing assistant: ` + err.Error())
		c.JSON(http.StatusInternalServerError, gin.H{`success`: false, `message`: "\u6682\u65f6\u65e0\u6cd5\u8bfb\u53d6\u5e73\u53f0\u4ef7\u683c\u7b56\u7565"})
		return
	}
	if intent.Query.ModelName == `` && intent.Query.ChannelID == nil && !intent.ListRequested {
		rows = nil
	} else if intent.Query.ModelName == `` && intent.Query.ChannelID == nil && len(rows) > 80 {
		rows = rows[:80]
	}
	c.JSON(http.StatusOK, gin.H{`success`: true, `data`: gin.H{
		`message`:                formatPricingAssistantReply(intent, rows, revision),
		`items`:                  rows,
		`language_model_context`: pricingAssistantLanguageModelContext(message, intent, rows, revision),
		`policy_revision`:        revision,
		`updated_at_ms`:          refreshedAtMS,
	}})
}

func playgroundPricingIntentFromMessage(message string) (playgroundPricingAssistantIntent, error) {
	catalog, err := agencyhub.LoadPlatformPricingCatalog(model.DB)
	if err != nil {
		return playgroundPricingAssistantIntent{}, err
	}
	intent := playgroundPricingAssistantIntent{}
	normalizedMessage := strings.ToLower(message)
	for _, item := range catalog.Items {
		if strings.Contains(normalizedMessage, strings.ToLower(item.OriginModelName)) {
			intent.Query.ModelName = item.OriginModelName
			break
		}
		for _, channel := range item.ChannelCosts {
			if channel.ChannelName != `` && strings.Contains(normalizedMessage, strings.ToLower(channel.ChannelName)) {
				channelID := channel.ChannelID
				intent.Query.ChannelID = &channelID
				break
			}
		}
		if intent.Query.ChannelID != nil {
			break
		}
	}
	if channelID := channelIDFromMessage(message); channelID > 0 {
		intent.Query.ChannelID = &channelID
	}
	// A general pricing question is still a data request. Previously only a
	// handful of phrases (such as "查看价格") populated the catalog, so
	// natural questions like "现在平台价格怎么样" reached the expression
	// layer with an empty result set.
	intent.ListRequested = containsAny(normalizedMessage, []string{
		`全部`, `所有`, `列表`, `清单`, `查看模型`, `查看价格`,
		`价格`, `定价`, `收费`, `成本`, `折扣`, `系数`,
		`all`, `list`, `price`, `pricing`, `cost`, `discount`,
	})
	intent.RulesRequested = containsAny(normalizedMessage, []string{
		`规则`, `说明`, `怎么计算`, `如何计算`, `策略`, `rule`, `policy`,
	})
	return intent, nil
}

func containsAny(value string, candidates []string) bool {
	for _, candidate := range candidates {
		if strings.Contains(value, candidate) {
			return true
		}
	}
	return false
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

func formatPricingAssistantReply(intent playgroundPricingAssistantIntent, rows []platformPricingMCPRow, revision int64) string {
	if intent.Query.ModelName == `` && intent.Query.ChannelID == nil && !intent.ListRequested {
		if intent.RulesRequested {
			return "### 平台价格策略说明\n\n- 平台成本系数按“模型 + 渠道”分别维护。\n- 仅返回公开模型和平台成本，不返回代理商或销售价格。\n- 输入模型名称、渠道名称或“渠道 ID + 编号”可查询实时配置。\n\n例如：`查询 deepseek-v4-flash`、`查询 Yunwoke-HappyHorse`、`查询渠道 ID 18 的价格`。"
		}
		return "我可以帮您查询具体模型或渠道的实时价格，不会调用模型或产生费用。\n\n例如：`查询 deepseek-v4-flash`、`查询 Yunwoke-HappyHorse`、`查询渠道 ID 18 的价格`。如需完整清单，请说“查看全部已配置价格”。"
	}
	if len(rows) == 0 {
		if intent.Query.ModelName != `` {
			return "未找到模型 " + intent.Query.ModelName + " 的当前可用价格策略。请确认模型名称或刷新渠道配置后重试。"
		}
		if intent.Query.ChannelID != nil {
			return "未找到渠道 ID " + strconv.Itoa(*intent.Query.ChannelID) + " 的当前可用价格策略。请确认渠道编号、启用状态和模型配置后重试。"
		}
		return "\u5f53\u524d\u6ca1\u6709\u53ef\u5c55\u793a\u7684\u6a21\u578b\u6e20\u9053\u4ef7\u683c\u7b56\u7565\u3002"
	}
	var builder strings.Builder
	if intent.Query.ModelName != `` {
		builder.WriteString("### " + intent.Query.ModelName + " 的价格策略\n\n")
	} else if intent.Query.ChannelID != nil {
		builder.WriteString("### 渠道 ID " + strconv.Itoa(*intent.Query.ChannelID) + " 的价格策略\n\n")
	} else {
		builder.WriteString("### 已配置模型与渠道价格\n\n")
	}
	builder.WriteString("已找到 " + strconv.Itoa(len(rows)) + " 条当前可用配置。\n\n")
	builder.WriteString("| \u6a21\u578b | \u6e20\u9053 | \u5e73\u53f0\u6210\u672c\u7cfb\u6570 |\n| --- | --- | ---: |\n")
	for _, row := range rows {
		builder.WriteString(`| `)
		builder.WriteString(row.ModelName)
		builder.WriteString(" | " + row.ChannelName + " (#" + strconv.Itoa(row.ChannelID) + ") | " + formatCoefficient(row.PlatformCostCoefficient) + " |\n")
	}
	builder.WriteString("\n数据版本：" + strconv.FormatInt(revision, 10) + "。")
	return builder.String()
}

func formatCoefficient(value *float64) string {
	if value == nil {
		return "\u672a\u8bbe\u7f6e"
	}
	return strconv.FormatFloat(*value, 'f', -1, 64)
}
