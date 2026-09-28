package controller

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/operation_setting"

	"github.com/gin-gonic/gin"
)

type logExportOther struct {
	CacheTokens           int                `json:"cache_tokens"`
	CacheCreationTokens   int                `json:"cache_creation_tokens"`
	CacheWriteTokens      int                `json:"cache_write_tokens"`
	CacheCreationTokens5m int                `json:"cache_creation_tokens_5m"`
	CacheCreationTokens1h int                `json:"cache_creation_tokens_1h"`
	ModelRatio            *float64           `json:"model_ratio"`
	ModelPrice            *float64           `json:"model_price"`
	CompletionRatio       *float64           `json:"completion_ratio"`
	CacheRatio            *float64           `json:"cache_ratio"`
	CacheCreationRatio    *float64           `json:"cache_creation_ratio"`
	GroupRatio            *float64           `json:"group_ratio"`
	UserGroupRatio        *float64           `json:"user_group_ratio"`
	OtherRatios           map[string]float64 `json:"other_ratios"`
	QuotaPerUnit          float64            `json:"quota_per_unit"`
	BillingMode           string             `json:"billing_mode"`
	MatchedTier           string             `json:"matched_tier"`
}

type logExportRow struct {
	Log      *model.Log
	Other    logExportOther
	Funding  model.LogExportFunding
	HasFunds bool
}

type logExportFundingColumns struct {
	Nonpaid bool
	Paid    bool
	Debt    bool
}

func logExportCSVSafe(value string) string {
	if value == "" {
		return value
	}
	trimmed := strings.TrimLeft(value, " \t\r\n")
	if trimmed == "" {
		return value
	}
	if strings.HasPrefix(trimmed, "=") || strings.HasPrefix(trimmed, "+") || strings.HasPrefix(trimmed, "@") {
		return "'" + value
	}
	if strings.HasPrefix(trimmed, "-") {
		if _, err := strconv.ParseFloat(strings.TrimSpace(value), 64); err != nil {
			return "'" + value
		}
	}
	return value
}

func parseLogExportOther(log *model.Log) (logExportOther, bool) {
	var other logExportOther
	if log == nil || log.Other == "" || common.UnmarshalJsonStr(log.Other, &other) != nil {
		return other, false
	}
	return other, true
}

func logExportMoneyPerMillion(other logExportOther) (string, string) {
	if other.BillingMode == "tiered_expr" || other.ModelRatio == nil || *other.ModelRatio < 0 ||
		(other.ModelPrice != nil && *other.ModelPrice > 0) {
		return "", ""
	}
	effectiveRatio := 1.0
	if other.GroupRatio != nil && *other.GroupRatio >= 0 &&
		!math.IsNaN(*other.GroupRatio) && !math.IsInf(*other.GroupRatio, 0) {
		effectiveRatio = *other.GroupRatio
	}
	if other.UserGroupRatio != nil && *other.UserGroupRatio >= 0 &&
		!math.IsNaN(*other.UserGroupRatio) && !math.IsInf(*other.UserGroupRatio, 0) {
		effectiveRatio = *other.UserGroupRatio
	}
	for _, ratio := range other.OtherRatios {
		if ratio > 0 && !math.IsNaN(ratio) && !math.IsInf(ratio, 0) {
			effectiveRatio *= ratio
		}
	}
	inputCNY := *other.ModelRatio * 2 * effectiveRatio * operation_setting.USDExchangeRate
	if math.IsNaN(inputCNY) || math.IsInf(inputCNY, 0) {
		return "", ""
	}
	if other.CompletionRatio == nil {
		return fmt.Sprintf("%.8f", inputCNY), ""
	}
	return fmt.Sprintf("%.8f", inputCNY), fmt.Sprintf("%.8f", inputCNY**other.CompletionRatio)
}

func logExportOptionalRatio(value *float64) string {
	if value == nil {
		return ""
	}
	return fmt.Sprintf("%.8f", *value)
}

func logExportActualMoney(log *model.Log, other logExportOther) (float64, bool) {
	if log == nil || other.QuotaPerUnit <= 0 || log.Quota < 0 {
		return 0, false
	}
	return float64(log.Quota) / other.QuotaPerUnit * operation_setting.USDExchangeRate, true
}

func writeLogExportRow(writer *csv.Writer, row logExportRow, columns logExportFundingColumns) error {
	other := row.Other
	inPrice, outPrice := logExportMoneyPerMillion(other)
	actual, hasActual := logExportActualMoney(row.Log, other)
	cacheRead := other.CacheTokens
	cacheWrite := other.CacheWriteTokens
	if cacheWrite == 0 {
		cacheWrite = other.CacheCreationTokens
	}
	if cacheWrite == 0 {
		cacheWrite = other.CacheCreationTokens5m + other.CacheCreationTokens1h
	}
	discount := other.MatchedTier
	values := []string{
		row.Log.Username, time.Unix(row.Log.CreatedAt, 0).Format("2006-01"), row.Log.ModelName,
		strconv.Itoa(row.Log.PromptTokens), strconv.Itoa(row.Log.CompletionTokens),
		strconv.Itoa(cacheRead), strconv.Itoa(cacheWrite),
		inPrice, outPrice,
		logExportOptionalRatio(other.CacheRatio), logExportOptionalRatio(other.CacheCreationRatio),
		discount,
	}
	if hasActual {
		values = append(values, fmt.Sprintf("%.8f", actual))
	} else {
		values = append(values, "")
	}
	if columns.Nonpaid {
		if other.QuotaPerUnit > 0 && row.HasFunds {
			fundingRate := operation_setting.USDExchangeRate / other.QuotaPerUnit
			values = append(values, fmt.Sprintf("%.8f", float64(row.Funding.NonpaidQuota)*fundingRate))
		} else {
			values = append(values, "")
		}
	}
	if columns.Paid {
		if other.QuotaPerUnit > 0 && row.HasFunds {
			fundingRate := operation_setting.USDExchangeRate / other.QuotaPerUnit
			values = append(values, fmt.Sprintf("%.8f", float64(row.Funding.PaidQuota)*fundingRate))
		} else {
			values = append(values, "")
		}
	}
	if columns.Debt {
		if other.QuotaPerUnit > 0 && row.HasFunds {
			fundingRate := operation_setting.USDExchangeRate / other.QuotaPerUnit
			values = append(values, fmt.Sprintf("%.8f", float64(row.Funding.DebtQuota)*fundingRate))
		} else {
			values = append(values, "")
		}
	}
	for i := range values {
		values[i] = logExportCSVSafe(values[i])
	}
	return writer.Write(values)
}

func writeLogExportSummary(writer *csv.Writer, label string, amount float64, columns logExportFundingColumns) error {
	values := []string{label}
	columnCount := 13
	if columns.Nonpaid {
		columnCount++
	}
	if columns.Paid {
		columnCount++
	}
	if columns.Debt {
		columnCount++
	}
	for len(values) < columnCount {
		values = append(values, "")
	}
	values[12] = fmt.Sprintf("%.8f", amount)
	return writer.Write(values)
}

func logExportQuery(c *gin.Context, isAdmin bool) model.LogExportParams {
	startTimestamp, _ := strconv.ParseInt(c.Query("start_timestamp"), 10, 64)
	endTimestamp, _ := strconv.ParseInt(c.Query("end_timestamp"), 10, 64)
	channel, _ := strconv.Atoi(c.Query("channel"))
	params := model.LogExportParams{
		IsAdmin:           isAdmin,
		StartTimestamp:    startTimestamp,
		EndTimestamp:      endTimestamp,
		ModelName:         c.Query("model_name"),
		TokenName:         c.Query("token_name"),
		Group:             c.Query("group"),
		RequestID:         c.Query("request_id"),
		UpstreamRequestID: c.Query("upstream_request_id"),
		Channel:           channel,
	}
	if isAdmin {
		params.Username = c.Query("username")
	} else {
		params.UserID = c.GetInt("id")
	}
	return params
}

func writeLogExportCSV(c *gin.Context, params model.LogExportParams) {
	logs, funding, err := model.GetConsumeLogsForExport(params)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, model.ErrLogExportTooManyRows) {
			status = http.StatusBadRequest
		}
		c.JSON(status, gin.H{"success": false, "message": err.Error()})
		return
	}

	rows := make([]logExportRow, 0, len(logs))
	var fundingColumns logExportFundingColumns
	for _, log := range logs {
		other, parsed := parseLogExportOther(log)
		fundingKey := model.LogExportFundingKey{UserID: int64(log.UserId), RequestID: log.RequestId}
		currentFunding, hasFunding := funding[fundingKey]
		if hasFunding && other.QuotaPerUnit > 0 {
			fundingColumns.Nonpaid = fundingColumns.Nonpaid || currentFunding.NonpaidQuota > 0
			fundingColumns.Paid = fundingColumns.Paid || currentFunding.PaidQuota > 0
			fundingColumns.Debt = fundingColumns.Debt || currentFunding.DebtQuota > 0
		}
		rows = append(rows, logExportRow{Log: log, Other: other, Funding: currentFunding, HasFunds: hasFunding})
		if !parsed {
			rows[len(rows)-1].Other = logExportOther{}
		}
	}

	var body bytes.Buffer
	body.WriteString("\xEF\xBB\xBF")
	writer := csv.NewWriter(&body)
	if err := writer.Write([]string{"按量消费明细"}); err != nil {
		common.ApiError(c, err)
		return
	}
	header := []string{"用户名称", "账期", "模型名称", "输入token", "输出token", "缓存读取token", "缓存创建token", "输入单价（元/M）", "输出单价（元/M）", "缓存读取倍率", "缓存创建倍率", "阶梯折扣", "实际消费（元）"}
	if fundingColumns.Nonpaid {
		header = append(header, "代金券抵扣（元）")
	}
	if fundingColumns.Paid {
		header = append(header, "充值余额支付（元）")
	}
	if fundingColumns.Debt {
		header = append(header, "授信额度支付（元）")
	}
	if err := writer.Write(header); err != nil {
		common.ApiError(c, err)
		return
	}

	var currentMonth string
	var monthAmount, totalAmount float64
	for _, row := range rows {
		month := time.Unix(row.Log.CreatedAt, 0).Format("2006-01")
		if currentMonth != "" && month != currentMonth {
			if err := writeLogExportSummary(writer, currentMonth+" 月度小计", monthAmount, fundingColumns); err != nil {
				common.ApiError(c, err)
				return
			}
			monthAmount = 0
		}
		currentMonth = month
		if actual, ok := logExportActualMoney(row.Log, row.Other); ok {
			monthAmount += actual
			totalAmount += actual
		}
		if err := writeLogExportRow(writer, row, fundingColumns); err != nil {
			common.ApiError(c, err)
			return
		}
	}
	if currentMonth != "" {
		if err := writeLogExportSummary(writer, currentMonth+" 月度小计", monthAmount, fundingColumns); err != nil {
			common.ApiError(c, err)
			return
		}
	}
	if err := writeLogExportSummary(writer, "本期消费金额", totalAmount, fundingColumns); err != nil {
		common.ApiError(c, err)
		return
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		common.ApiError(c, err)
		return
	}
	c.Header("Content-Type", "text/csv; charset=utf-8")
	c.Header("Content-Disposition", "attachment; filename="+strconv.Quote("usage-bill.csv"))
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, "text/csv; charset=utf-8", body.Bytes())
}

func GetAllLogsExport(c *gin.Context) {
	writeLogExportCSV(c, logExportQuery(c, true))
}

func GetUserLogsExport(c *gin.Context) {
	writeLogExportCSV(c, logExportQuery(c, false))
}

func GetAllLogs(c *gin.Context) {
	pageInfo := common.GetPageQuery(c)
	logType, _ := strconv.Atoi(c.Query("type"))
	startTimestamp, _ := strconv.ParseInt(c.Query("start_timestamp"), 10, 64)
	endTimestamp, _ := strconv.ParseInt(c.Query("end_timestamp"), 10, 64)
	username := c.Query("username")
	tokenName := c.Query("token_name")
	modelName := c.Query("model_name")
	channel, _ := strconv.Atoi(c.Query("channel"))
	group := c.Query("group")
	requestId := c.Query("request_id")
	upstreamRequestId := c.Query("upstream_request_id")
	logs, total, err := model.GetAllLogs(logType, startTimestamp, endTimestamp, modelName, username, tokenName, pageInfo.GetStartIdx(), pageInfo.GetPageSize(), channel, group, requestId, upstreamRequestId)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	if c.GetInt("role") < common.RoleRootUser {
		model.FormatAdminLogs(logs)
	} else {
		model.FormatRootLogs(logs)
	}
	pageInfo.SetTotal(int(total))
	pageInfo.SetItems(logs)
	common.ApiSuccess(c, pageInfo)
	return
}

func GetUserLogs(c *gin.Context) {
	pageInfo := common.GetPageQuery(c)
	userId := c.GetInt("id")
	logType, _ := strconv.Atoi(c.Query("type"))
	startTimestamp, _ := strconv.ParseInt(c.Query("start_timestamp"), 10, 64)
	endTimestamp, _ := strconv.ParseInt(c.Query("end_timestamp"), 10, 64)
	tokenName := c.Query("token_name")
	modelName := c.Query("model_name")
	group := c.Query("group")
	requestId := c.Query("request_id")
	upstreamRequestId := c.Query("upstream_request_id")
	logs, total, err := model.GetUserLogs(userId, logType, startTimestamp, endTimestamp, modelName, tokenName, pageInfo.GetStartIdx(), pageInfo.GetPageSize(), group, requestId, upstreamRequestId)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	pageInfo.SetTotal(int(total))
	pageInfo.SetItems(logs)
	common.ApiSuccess(c, pageInfo)
	return
}

// Deprecated: SearchAllLogs 已废弃，前端未使用该接口。
func SearchAllLogs(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"success": false,
		"message": "该接口已废弃",
	})
}

// Deprecated: SearchUserLogs 已废弃，前端未使用该接口。
func SearchUserLogs(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"success": false,
		"message": "该接口已废弃",
	})
}

func GetLogByKey(c *gin.Context) {
	tokenId := c.GetInt("token_id")
	if tokenId == 0 {
		c.JSON(200, gin.H{
			"success": false,
			"message": "无效的令牌",
		})
		return
	}
	logs, err := model.GetLogByTokenId(tokenId)
	if err != nil {
		c.JSON(200, gin.H{
			"success": false,
			"message": err.Error(),
		})
		return
	}
	c.JSON(200, gin.H{
		"success": true,
		"message": "",
		"data":    logs,
	})
}

func GetLogsStat(c *gin.Context) {
	logType, _ := strconv.Atoi(c.Query("type"))
	startTimestamp, _ := strconv.ParseInt(c.Query("start_timestamp"), 10, 64)
	endTimestamp, _ := strconv.ParseInt(c.Query("end_timestamp"), 10, 64)
	tokenName := c.Query("token_name")
	username := c.Query("username")
	modelName := c.Query("model_name")
	channel, _ := strconv.Atoi(c.Query("channel"))
	group := c.Query("group")
	stat, err := model.SumUsedQuota(logType, startTimestamp, endTimestamp, modelName, username, tokenName, channel, group)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	//tokenNum := model.SumUsedToken(logType, startTimestamp, endTimestamp, modelName, username, "")
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"quota": stat.Quota,
			"rpm":   stat.Rpm,
			"tpm":   stat.Tpm,
		},
	})
	return
}

func GetLogsSelfStat(c *gin.Context) {
	username := c.GetString("username")
	logType, _ := strconv.Atoi(c.Query("type"))
	startTimestamp, _ := strconv.ParseInt(c.Query("start_timestamp"), 10, 64)
	endTimestamp, _ := strconv.ParseInt(c.Query("end_timestamp"), 10, 64)
	tokenName := c.Query("token_name")
	modelName := c.Query("model_name")
	channel, _ := strconv.Atoi(c.Query("channel"))
	group := c.Query("group")
	quotaNum, err := model.SumUsedQuota(logType, startTimestamp, endTimestamp, modelName, username, tokenName, channel, group)
	if err != nil {
		common.ApiError(c, err)
		return
	}
	//tokenNum := model.SumUsedToken(logType, startTimestamp, endTimestamp, modelName, username, tokenName)
	c.JSON(200, gin.H{
		"success": true,
		"message": "",
		"data": gin.H{
			"quota": quotaNum.Quota,
			"rpm":   quotaNum.Rpm,
			"tpm":   quotaNum.Tpm,
			//"token": tokenNum,
		},
	})
	return
}
