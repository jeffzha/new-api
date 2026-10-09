package agencyhub

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type platformPricingPublishRequest struct {
	ExpectedRevision int64                               `json:"expected_revision"`
	ModelPrices      []agencycontract.PlatformModelPrice `json:"model_prices"`
	Reason           string                              `json:"reason"`
}

type liveModelChannel struct {
	Model       string
	Group       string
	ChannelID   int
	ChannelName string
}

type platformPricingAgencyPolicy struct {
	AgencyID       int64
	ParentAgencyID *int64
	DisplayName    string
	PolicyJSON     string
}

type platformPricingConflictError struct {
	Conflicts []string
}

func (e *platformPricingConflictError) Error() string {
	return strings.Join(e.Conflicts, "；")
}

func validatePlatformPricingCustomerSales(db *gorm.DB, policy agencycontract.PlatformPolicy) ([]string, error) {
	var activePolicies []platformPricingAgencyPolicy
	if err := db.Table((model.Agency{}).TableName() + " AS agency").
		Select("agency.id AS agency_id, agency.parent_agency_id, agency.display_name, version.policy_json").
		Joins("JOIN " + (model.AgencyPricePolicyVersion{}).TableName() + " AS version ON version.id = agency.current_policy_version_id AND version.agency_id = agency.id").
		Scan(&activePolicies).Error; err != nil {
		return nil, err
	}

	conflicts := make([]string, 0)
	for _, row := range activePolicies {
		var agencyPolicy agencycontract.Policy
		if err := common.Unmarshal([]byte(row.PolicyJSON), &agencyPolicy); err != nil {
			return nil, fmt.Errorf("代理商价格策略数据异常：%s", row.DisplayName)
		}
		var validationErr error
		effectivePolicy := agencyPolicy
		if row.ParentAgencyID == nil {
			effectivePolicy, validationErr = agencycontract.ApplyPlatformPolicy(agencyPolicy, policy)
		} else {
			validationErr = agencycontract.ValidatePolicy(agencyPolicy)
		}
		if validationErr == nil {
			validationErr = validateCustomerSalesPolicyTx(db, row.AgencyID, effectivePolicy)
		}
		if validationErr != nil {
			conflicts = append(conflicts, row.DisplayName+"："+pricingErrorMessage(validationErr))
		}
	}
	return conflicts, nil
}

type PlatformPricingCatalogChannel struct {
	ChannelID       int    `json:"channel_id"`
	ChannelName     string `json:"channel_name"`
	Available       bool   `json:"available"`
	PlatformCostBPS *int   `json:"platform_cost_bps"`
}

type PlatformPricingCatalogRow struct {
	// VendorName and VendorIcon mirror the model square provider so the
	// pricing matrix filters by the vendor an agency actually resells.
	VendorName      string                          `json:"vendor_name,omitempty"`
	VendorIcon      string                          `json:"vendor_icon,omitempty"`
	OriginModelName string                          `json:"origin_model_name"`
	ChannelNames    []string                        `json:"channel_names"`
	ChannelCosts    []PlatformPricingCatalogChannel `json:"channel_costs"`
	// PlatformCostBPS is a read-only legacy fallback for a policy published
	// before per-channel platform costs were introduced.
	PlatformCostBPS *int `json:"platform_cost_bps"`
	AgencyCostBPS   *int `json:"agency_cost_bps"`
	ChildCostBPS    *int `json:"child_cost_bps"`
	DefaultSalesBPS *int `json:"default_sales_bps"`
}

// PlatformPricingCatalog is the current, read-only view of the pricing
// policy combined with live model/channel abilities. Both the agency hub and
// internal integrations use it so they cannot drift apart.
type PlatformPricingCatalog struct {
	Revision      int64                       `json:"revision"`
	Items         []PlatformPricingCatalogRow `json:"items"`
	RefreshedAtMS int64                       `json:"refreshed_at_ms"`
}

func loadLiveModelChannels(db *gorm.DB) ([]liveModelChannel, error) {
	var rows []liveModelChannel
	err := db.Table("abilities").
		Select("abilities.*, channels.name AS channel_name").
		Joins("JOIN channels ON channels.id = abilities.channel_id").
		Where("abilities.enabled = ? AND channels.status = ?", true, common.ChannelStatusEnabled).
		Order("abilities.model ASC, channels.name ASC, abilities.channel_id ASC").
		Scan(&rows).Error
	return rows, err
}

// publicLiveModelChannels drops abilities that only route platform-internal
// traffic, so mock upstreams never reach agency or customer catalogs.
func publicLiveModelChannels(rows []liveModelChannel) []liveModelChannel {
	live := make([]liveModelChannel, 0, len(rows))
	for _, row := range rows {
		if ratio_setting.IsInternalGroup(row.Group) {
			continue
		}
		live = append(live, row)
	}
	return live
}

// internalChannelIDs lists channels whose enabled abilities all belong to
// internal routing groups. Costs published for them must stay hidden even if
// they were configured while the channel was still public.
func internalChannelIDs(rows []liveModelChannel) map[int]struct{} {
	internal := make(map[int]struct{})
	public := make(map[int]struct{})
	for _, row := range rows {
		if ratio_setting.IsInternalGroup(row.Group) {
			internal[row.ChannelID] = struct{}{}
			continue
		}
		public[row.ChannelID] = struct{}{}
	}
	for channelID := range public {
		delete(internal, channelID)
	}
	return internal
}

// publicChannelCosts keeps only the configured costs that belong to channels
// reachable through a public routing group.
func publicChannelCosts(costs []agencycontract.PlatformChannelCost, internalChannels map[int]struct{}) []agencycontract.PlatformChannelCost {
	public := make([]agencycontract.PlatformChannelCost, 0, len(costs))
	for _, cost := range costs {
		if _, internal := internalChannels[cost.ChannelID]; internal {
			continue
		}
		public = append(public, cost)
	}
	return public
}

// liveModelChannelSummary groups the enabled channels that route each model
// through a public group. Channels that only serve internal routing groups
// stay hidden, so an agency can filter its pricing table by the same channel
// names the platform catalog shows.
func liveModelChannelSummary(rows []liveModelChannel) map[string][]gin.H {
	summary := make(map[string][]gin.H)
	seen := make(map[string]map[int]struct{})
	for _, row := range publicLiveModelChannels(rows) {
		channels := seen[row.Model]
		if channels == nil {
			channels = make(map[int]struct{})
			seen[row.Model] = channels
		}
		if _, exists := channels[row.ChannelID]; exists {
			continue
		}
		channels[row.ChannelID] = struct{}{}
		summary[row.Model] = append(summary[row.Model], gin.H{"channel_id": row.ChannelID, "channel_name": row.ChannelName})
	}
	return summary
}

// priceVisibleToAgencies hides a configured price only when every one of its
// channels is internal and no public ability routes the model. Configured
// prices without channel costs stay visible for backward compatibility.
func priceVisibleToAgencies(price agencycontract.PlatformModelPrice, hasPublicAbility bool, internalChannels map[int]struct{}) bool {
	if hasPublicAbility || len(price.ChannelCosts) == 0 {
		return true
	}
	return len(publicChannelCosts(price.ChannelCosts, internalChannels)) > 0
}

func LoadPlatformPricingCatalog(db *gorm.DB) (*PlatformPricingCatalog, error) {
	policy, err := model.LoadAgencyPlatformPolicy(db)
	if err != nil {
		return nil, err
	}
	rows, err := loadLiveModelChannels(db)
	if err != nil {
		return nil, err
	}
	live := publicLiveModelChannels(rows)
	internalChannels := internalChannelIDs(rows)
	configured := make(map[string]agencycontract.PlatformModelPrice, len(policy.ModelPrices))
	publicModels := make(map[string]struct{}, len(live))
	for _, item := range live {
		publicModels[item.Model] = struct{}{}
	}
	for _, price := range policy.ModelPrices {
		configured[price.OriginModelName] = price
	}
	byModel := make(map[string]*PlatformPricingCatalogRow)
	order := make([]string, 0)
	for _, item := range live {
		row := byModel[item.Model]
		if row == nil {
			row = &PlatformPricingCatalogRow{OriginModelName: item.Model}
			if price, ok := configured[item.Model]; ok {
				agencyCost, defaultSales := price.AgencyCostBPS, price.DefaultSalesBPS
				row.AgencyCostBPS, row.DefaultSalesBPS = &agencyCost, &defaultSales
				if len(price.ChannelCosts) == 0 {
					legacyCost := price.PlatformCostBPS
					row.PlatformCostBPS = &legacyCost
				}
			}
			byModel[item.Model] = row
			order = append(order, item.Model)
		}
		if containsCatalogChannel(row.ChannelCosts, item.ChannelID) {
			continue
		}
		var channelCost *int
		if price, configuredNow := configured[item.Model]; configuredNow {
			channelCost = configuredChannelCost(price, item.ChannelID)
		}
		row.ChannelNames = append(row.ChannelNames, item.ChannelName)
		row.ChannelCosts = append(row.ChannelCosts, PlatformPricingCatalogChannel{ChannelID: item.ChannelID, ChannelName: item.ChannelName, Available: true, PlatformCostBPS: channelCost})
	}
	// Keep a configured row visible even if its channel was disabled after publication.
	for name, price := range configured {
		row := byModel[name]
		_, hasPublicAbility := publicModels[name]
		if !priceVisibleToAgencies(price, hasPublicAbility, internalChannels) {
			// A model priced only through internal channels must not reappear
			// in any outward facing catalog.
			continue
		}
		publicCosts := publicChannelCosts(price.ChannelCosts, internalChannels)
		if row == nil {
			agencyCost, defaultSales := price.AgencyCostBPS, price.DefaultSalesBPS
			row = &PlatformPricingCatalogRow{OriginModelName: name, ChannelNames: []string{}, AgencyCostBPS: &agencyCost, DefaultSalesBPS: &defaultSales}
			if len(price.ChannelCosts) == 0 {
				legacyCost := price.PlatformCostBPS
				row.PlatformCostBPS = &legacyCost
			}
			byModel[name] = row
			order = append(order, name)
		}
		for _, cost := range publicCosts {
			if containsCatalogChannel(row.ChannelCosts, cost.ChannelID) {
				continue
			}
			costBPS := cost.PlatformCostBPS
			row.ChannelCosts = append(row.ChannelCosts, PlatformPricingCatalogChannel{ChannelID: cost.ChannelID, ChannelName: "渠道当前不可用 #" + strconv.Itoa(cost.ChannelID), Available: false, PlatformCostBPS: &costBPS})
		}
	}
	sort.Strings(order)
	// Provider names only decorate the matrix, so a metadata read failure must
	// never take the pricing catalog down.
	vendors, vendorErr := model.ResolveModelVendors(db, order)
	if vendorErr != nil {
		common.SysLog("agency hub pricing: resolve model vendors failed: " + vendorErr.Error())
		vendors = nil
	}
	items := make([]PlatformPricingCatalogRow, 0, len(order))
	for _, name := range order {
		if vendor, ok := vendors[name]; ok {
			byModel[name].VendorName = vendor.Name
			byModel[name].VendorIcon = vendor.Icon
		}
		items = append(items, *byModel[name])
	}
	return &PlatformPricingCatalog{Revision: policy.Revision, Items: items, RefreshedAtMS: time.Now().UnixMilli()}, nil
}

func (a *App) getPlatformPricing(c *gin.Context) {
	catalog, err := LoadPlatformPricingCatalog(a.db)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "database_error", "读取平台价格策略失败", nil)
		return
	}
	respondOK(c, catalog)
}

func containsCatalogChannel(costs []PlatformPricingCatalogChannel, channelID int) bool {
	for _, cost := range costs {
		if cost.ChannelID == channelID {
			return true
		}
	}
	return false
}

func configuredChannelCost(price agencycontract.PlatformModelPrice, channelID int) *int {
	if len(price.ChannelCosts) == 0 {
		cost := price.PlatformCostBPS
		return &cost
	}
	for _, cost := range price.ChannelCosts {
		if cost.ChannelID == channelID {
			configured := cost.PlatformCostBPS
			return &configured
		}
	}
	return nil
}

func (a *App) publishPlatformPricing(c *gin.Context) {
	var request platformPricingPublishRequest
	if err := common.DecodeJsonStrict(c.Request.Body, &request); err != nil {
		respondError(c, http.StatusBadRequest, "invalid_request", "请求格式错误", nil)
		return
	}
	request.Reason = strings.TrimSpace(request.Reason)
	if len(request.Reason) > 2000 {
		respondError(c, http.StatusUnprocessableEntity, "invalid_reason", "价格调整原因不能超过2000字节", nil)
		return
	}
	policy := agencycontract.PlatformPolicy{ModelPrices: request.ModelPrices}
	if err := agencycontract.ValidatePlatformPolicy(policy); err != nil {
		respondError(c, http.StatusUnprocessableEntity, "invalid_pricing", pricingErrorMessage(err), nil)
		return
	}
	previousPolicy, err := model.LoadAgencyPlatformPolicy(a.db)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "database_error", "读取当前平台价格策略失败", nil)
		return
	}
	rows, err := loadLiveModelChannels(a.db)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "database_error", "读取实时模型与渠道失败", nil)
		return
	}
	live := publicLiveModelChannels(rows)
	internalChannels := internalChannelIDs(rows)
	liveModels := make(map[string]struct{}, len(live))
	liveChannels := make(map[string]map[int]struct{}, len(live))
	for _, item := range live {
		liveModels[item.Model] = struct{}{}
		if liveChannels[item.Model] == nil {
			liveChannels[item.Model] = make(map[int]struct{})
		}
		liveChannels[item.Model][item.ChannelID] = struct{}{}
	}
	previousModels := make(map[string]struct{}, len(previousPolicy.ModelPrices))
	previousChannelCosts := make(map[string]map[int]struct{}, len(previousPolicy.ModelPrices))
	for _, price := range previousPolicy.ModelPrices {
		previousModels[price.OriginModelName] = struct{}{}
		for _, cost := range price.ChannelCosts {
			if previousChannelCosts[price.OriginModelName] == nil {
				previousChannelCosts[price.OriginModelName] = make(map[int]struct{})
			}
			previousChannelCosts[price.OriginModelName][cost.ChannelID] = struct{}{}
		}
	}
	for _, price := range policy.ModelPrices {
		_, liveNow := liveModels[price.OriginModelName]
		_, configuredBefore := previousModels[price.OriginModelName]
		if !liveNow && !configuredBefore {
			respondError(c, http.StatusConflict, "model_unavailable", "模型已不在启用渠道中，请刷新后重试："+price.OriginModelName, nil)
			return
		}
		if len(price.ChannelCosts) == 0 {
			continue
		}
		for _, cost := range price.ChannelCosts {
			if _, internal := internalChannels[cost.ChannelID]; internal {
				respondError(c, http.StatusConflict, "channel_unavailable", "渠道已不属于该模型或当前不可用，请刷新后重试。", nil)
				return
			}
			_, liveNow := liveChannels[price.OriginModelName][cost.ChannelID]
			_, configuredBefore := previousChannelCosts[price.OriginModelName][cost.ChannelID]
			if liveNow || configuredBefore {
				continue
			}
			respondError(c, http.StatusConflict, "channel_unavailable", "渠道已不属于该模型或当前不可用，请刷新后重试。", nil)
			return
		}
		if active := liveChannels[price.OriginModelName]; len(active) > 0 {
			configured := make(map[int]struct{}, len(price.ChannelCosts))
			for _, cost := range price.ChannelCosts {
				configured[cost.ChannelID] = struct{}{}
			}
			for channelID := range active {
				if _, ok := configured[channelID]; !ok {
					respondError(c, http.StatusUnprocessableEntity, "channel_cost_required", "已配置的模型必须为每个启用渠道填写平台成本系数。", nil)
					return
				}
			}
		}
	}
	conflicts, err := validatePlatformPricingCustomerSales(a.db, policy)
	if err != nil {
		respondError(c, http.StatusInternalServerError, "database_error", "校验代理商价格策略失败", nil)
		return
	}
	if len(conflicts) > 0 {
		respondError(c, http.StatusConflict, "agency_sales_below_cost", "以下代理商的价格策略不符合要求，请先调整："+strings.Join(conflicts, "；"), nil)
		return
	}
	identity := currentIdentity(c)
	now := time.Now().UnixMilli()
	err = a.db.Transaction(func(tx *gorm.DB) error {
		// Customer price changes lock their agency first. Taking the same locks
		// here serializes the final validation with concurrent customer updates.
		var agencies []model.Agency
		if lockErr := model.AgencyLockForUpdate(tx).Order("id ASC").Find(&agencies).Error; lockErr != nil {
			return lockErr
		}
		transactionConflicts, validationErr := validatePlatformPricingCustomerSales(tx, policy)
		if validationErr != nil {
			return validationErr
		}
		if len(transactionConflicts) > 0 {
			return &platformPricingConflictError{Conflicts: transactionConflicts}
		}
		var state model.AgencyPlatformPriceState
		findErr := tx.First(&state, 1).Error
		if errors.Is(findErr, gorm.ErrRecordNotFound) {
			if request.ExpectedRevision != 0 {
				return agencycontract.ErrPolicyRevision
			}
			state = model.AgencyPlatformPriceState{ID: 1}
		} else if findErr != nil {
			return findErr
		} else if state.Revision != request.ExpectedRevision {
			return agencycontract.ErrPolicyRevision
		}
		policy.Revision = request.ExpectedRevision + 1
		encoded, marshalErr := common.Marshal(policy)
		if marshalErr != nil {
			return marshalErr
		}
		digest := sha256.Sum256(encoded)
		version := model.AgencyPlatformPriceVersion{Revision: policy.Revision, PolicyJSON: string(encoded), PolicyHash: hex.EncodeToString(digest[:]), CreatedByID: identity.ActorID, Reason: request.Reason, CreatedAtMS: now}
		if createErr := tx.Create(&version).Error; createErr != nil {
			return createErr
		}
		if state.Revision == 0 {
			state.Revision, state.CurrentVersionID, state.UpdatedAtMS = policy.Revision, version.ID, now
			if createErr := tx.Create(&state).Error; createErr != nil {
				return createErr
			}
		} else {
			result := tx.Model(&state).Where("id = ? AND revision = ?", state.ID, request.ExpectedRevision).Updates(map[string]any{"revision": policy.Revision, "current_version_id": version.ID, "updated_at_ms": now})
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return agencycontract.ErrPolicyRevision
			}
		}
		return recordAuditTx(tx, c, identity, "pricing.platform.publish", "platform_pricing", "current", request.Reason, gin.H{"revision": request.ExpectedRevision}, gin.H{"revision": policy.Revision, "model_count": len(policy.ModelPrices)})
	})
	if err != nil {
		var conflictErr *platformPricingConflictError
		if errors.As(err, &conflictErr) {
			respondError(c, http.StatusConflict, "agency_sales_below_cost", "以下代理商的价格策略不符合要求，请先调整："+conflictErr.Error(), nil)
			return
		}
		if errors.Is(err, agencycontract.ErrPolicyRevision) {
			respondError(c, http.StatusConflict, "price_revision_conflict", "平台价格策略已更新，请刷新后重试", nil)
			return
		}
		respondError(c, http.StatusInternalServerError, "publish_failed", "发布平台价格策略失败", nil)
		return
	}
	respondOK(c, gin.H{"revision": policy.Revision, "committed_at_ms": now})
}
