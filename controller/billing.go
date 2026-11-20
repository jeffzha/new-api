package controller

import (
	"errors"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/opsauthority"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
)

func GetSubscription(c *gin.Context) {
	var remainQuota int
	var usedQuota int
	var err error
	var token *model.Token
	var expiredTime int64
	if common.DisplayTokenStatEnabled {
		tokenId := c.GetInt("token_id")
		token, err = model.GetTokenById(tokenId)
		if err == nil && token != nil {
			expiredTime = token.ExpiredTime
			remainQuota = token.RemainQuota
			usedQuota = token.UsedQuota
		} else if err == nil {
			err = errors.New("API token unavailable")
		}
	} else {
		userId := c.GetInt("id")
		if opsauthority.Enabled() {
			balance := walletBalance(userId)
			if balance.Status != "available" || balance.Quota == nil || balance.Stale {
				err = errors.New("OPS balance unavailable")
			} else {
				remainQuota = int(*balance.Quota)
			}
		} else {
			remainQuota, err = model.GetUserQuota(userId, false)
		}
		if err == nil {
			usedQuota, err = model.GetUserUsedQuota(userId)
		}
	}
	if expiredTime <= 0 {
		expiredTime = 0
	}
	if err != nil {
		openAIError := types.OpenAIError{
			Message: err.Error(),
			Type:    "upstream_error",
		}
		c.JSON(200, gin.H{
			"error": openAIError,
		})
		return
	}
	// Convert before adding: wallet and lifetime usage may each be large.
	amount := float64(remainQuota) + float64(usedQuota)
	// OpenAI 兼容接口中的 *_USD 字段含义保持“额度单位”对应值：
	// 我们将其解释为以“站点展示类型”为准：
	// - USD: 直接除以 QuotaPerUnit
	// - CNY: 先转 USD 再乘汇率
	// - TOKENS: 直接使用 tokens 数量
	switch operation_setting.GetQuotaDisplayType() {
	case operation_setting.QuotaDisplayTypeCNY:
		amount = amount / common.QuotaPerUnit * operation_setting.USDExchangeRate
	case operation_setting.QuotaDisplayTypeTokens:
		// amount 保持 tokens 数值
	default:
		amount = amount / common.QuotaPerUnit
	}
	if token != nil && token.UnlimitedQuota {
		amount = 100000000
	}
	subscription := OpenAISubscriptionResponse{
		Object:             "billing_subscription",
		HasPaymentMethod:   true,
		SoftLimitUSD:       amount,
		HardLimitUSD:       amount,
		SystemHardLimitUSD: amount,
		AccessUntil:        expiredTime,
	}
	c.JSON(200, subscription)
	return
}

func GetUsage(c *gin.Context) {
	var quota int
	var err error
	var token *model.Token
	if common.DisplayTokenStatEnabled {
		tokenId := c.GetInt("token_id")
		token, err = model.GetTokenById(tokenId)
		if err == nil && token != nil {
			quota = token.UsedQuota
		} else if err == nil {
			err = errors.New("API token unavailable")
		}
	} else {
		userId := c.GetInt("id")
		quota, err = model.GetUserUsedQuota(userId)
	}
	if err != nil {
		openAIError := types.OpenAIError{
			Message: err.Error(),
			Type:    "new_api_error",
		}
		c.JSON(200, gin.H{
			"error": openAIError,
		})
		return
	}
	amount := float64(quota)
	switch operation_setting.GetQuotaDisplayType() {
	case operation_setting.QuotaDisplayTypeCNY:
		amount = amount / common.QuotaPerUnit * operation_setting.USDExchangeRate
	case operation_setting.QuotaDisplayTypeTokens:
		// tokens 保持原值
	default:
		amount = amount / common.QuotaPerUnit
	}
	usage := OpenAIUsageResponse{
		Object:     "list",
		TotalUsage: amount * 100,
	}
	c.JSON(200, usage)
	return
}
