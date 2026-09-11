package controller

import (
	"errors"
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/service"
	hosttypes "github.com/QuantumNous/new-api/types"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func agencySchemaUnavailable(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "no such table") ||
		strings.Contains(message, "doesn't exist") ||
		strings.Contains(message, "does not exist") ||
		strings.Contains(message, "undefined table")
}

// resolveAgencyPrice runs the existing model pricing engine twice for a
// managed customer: once with a neutral ratio to capture the standard quota
// Q, then with the immutable agency sales coefficient to produce B. This
// keeps all protocol-specific rounding and expression logic in the existing
// engine instead of duplicating it in the agency package.
func resolveAgencyPrice(
	c *gin.Context,
	info *relaycommon.RelayInfo,
	calculate func() (hosttypes.PriceData, error),
) (hosttypes.PriceData, error) {
	if info == nil || strings.TrimSpace(info.OriginModelName) == "" {
		return calculate()
	}

	snapshot, err := service.AgencyQuoteForUser(info.UserId, info.TokenId, info.OriginModelName, info.StartTime.UnixMilli())
	if errors.Is(err, gorm.ErrRecordNotFound) {
		// A durable user must never be silently re-routed to the legacy
		// calculator when its agency binding/schema is unavailable.  Legacy
		// users (which have no durable billing mode) retain the old path.
		if model.IsAgencyDurableUser(info.UserId) {
			return hosttypes.PriceData{}, fmt.Errorf("agency pricing unavailable for durable user: %w", err)
		}
		return calculate()
	}
	if agencySchemaUnavailable(err) {
		if model.IsAgencyDurableUser(info.UserId) {
			return hosttypes.PriceData{}, fmt.Errorf("agency pricing schema unavailable for durable user: %w", err)
		}
		return calculate()
	}
	if err != nil {
		return hosttypes.PriceData{}, err
	}
	defer clearAgencyRatioOverride(c)

	// Q is evaluated with a neutral group ratio. The second pass applies S;
	// both passes share the same request body, token estimate, and model
	// expression, so their rounding behavior remains identical to new-api.
	c.Set(helper.AgencyRatioOverrideContextKey, float64(1))
	standardPrice, err := calculate()
	if err != nil {
		return hosttypes.PriceData{}, err
	}
	standardQuota := int64(standardPrice.QuotaToPreConsume)

	c.Set(helper.AgencyRatioOverrideContextKey, float64(snapshot.SalesBPS)/10000)
	chargedPrice, err := calculate()
	if err != nil {
		return hosttypes.PriceData{}, err
	}
	if err := service.AttachAgencyQuote(info, snapshot, standardQuota); err != nil {
		return hosttypes.PriceData{}, err
	}
	return chargedPrice, nil
}

// clearAgencyRatioOverride prevents a request-local context value from
// affecting unrelated pricing calculations after the agency bridge returns.
func clearAgencyRatioOverride(c *gin.Context) {
	if c != nil {
		c.Set(helper.AgencyRatioOverrideContextKey, nil)
	}
}
