package controller

import (
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
	Log              *model.Log
	Other            logExportOther
	Funding          model.LogExportFunding
	HasFunds         bool
	AgencyPricing    model.LogExportAgencyPricing
	HasAgencyPricing bool
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

func logExportQuotaMoney(quota int64, quotaPerUnit, exchangeRate string) (float64, bool) {
	unit, unitErr := strconv.ParseFloat(quotaPerUnit, 64)
	rate, rateErr := strconv.ParseFloat(exchangeRate, 64)
	if quota < 0 || unitErr != nil || rateErr != nil || unit <= 0 || rate < 0 ||
		math.IsNaN(unit) || math.IsInf(unit, 0) || math.IsNaN(rate) || math.IsInf(rate, 0) {
		return 0, false
	}
	return float64(quota) / unit * rate, true
}

func logExportRowActualMoney(row logExportRow) (float64, bool) {
	if row.HasAgencyPricing {
		if amount, ok := logExportQuotaMoney(row.AgencyPricing.ChargedTotalQuota, row.AgencyPricing.QuotaPerUnit, row.AgencyPricing.ExchangeRate); ok {
			return amount, true
		}
	}
	return logExportActualMoney(row.Log, row.Other)
}

func logExportRowValues(row logExportRow) []string {
	other := row.Other
	inPrice, outPrice := logExportMoneyPerMillion(other)
	actual, hasActual := logExportRowActualMoney(row)
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
	fundingValues := []int64{row.Funding.NonpaidQuota, row.Funding.PaidQuota, row.Funding.DebtQuota}
	for _, quota := range fundingValues {
		if other.QuotaPerUnit > 0 && row.HasFunds {
			fundingRate := operation_setting.USDExchangeRate / other.QuotaPerUnit
			values = append(values, fmt.Sprintf("%.8f", float64(quota)*fundingRate))
		} else {
			values = append(values, "")
		}
	}
	usedAgencyDiscount := "否"
	agencySalesRatio := ""
	agencyDiscountMoney := ""
	if row.HasAgencyPricing {
		agencySalesRatio = fmt.Sprintf("%.4f", float64(row.AgencyPricing.SalesBPS)/10000)
		discountQuota := max(row.AgencyPricing.StandardQuota-row.AgencyPricing.ChargedTotalQuota, 0)
		if discountQuota > 0 {
			usedAgencyDiscount = "是"
		}
		if amount, ok := logExportQuotaMoney(discountQuota, row.AgencyPricing.QuotaPerUnit, row.AgencyPricing.ExchangeRate); ok {
			agencyDiscountMoney = fmt.Sprintf("%.8f", amount)
		}
	}
	values = append(values, usedAgencyDiscount, agencySalesRatio, agencyDiscountMoney)
	for i := range values {
		values[i] = logExportCSVSafe(values[i])
	}
	return values
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
	if isAdmin && c.GetInt("role") >= common.RoleRootUser {
		params.Username = strings.TrimSpace(c.Query("username"))
	} else if !isAdmin {
		params.UserID = c.GetInt("id")
	}
	return params
}

func GetAllLogsExport(c *gin.Context) {
	writeLogExport(c, logExportQuery(c, true))
}

func GetUserLogsExport(c *gin.Context) {
	writeLogExport(c, logExportQuery(c, false))
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
