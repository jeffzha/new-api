package service

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/QuantumNous/new-api/pkg/agencyhub"
	"github.com/QuantumNous/new-api/service/opsbridge"
	"gorm.io/gorm"
)

type OpsAgencyCommandExecutor struct{}

func (OpsAgencyCommandExecutor) ExecuteOpsAgencyCommand(ctx context.Context, command opsbridge.OpsAgencyCommand) (map[string]any, error) {
	switch command.Kind {
	case "provision_customer":
		return executeOpsProvisionCustomer(ctx, command)
	case "suspend_customer":
		return executeOpsCustomerStatus(ctx, command, common.UserStatusDisabled)
	case "resume_customer":
		return executeOpsCustomerStatus(ctx, command, common.UserStatusEnabled)
	case "sync_agency_policy":
		return executeOpsAgencyPolicy(ctx, command)
	case "transition_writer_mode":
		return executeOpsWriterTransition(ctx, command)
	default:
		return nil, fmt.Errorf("unsupported TokenOps agency command: %s", command.Kind)
	}
}

func executeOpsWriterTransition(ctx context.Context, command opsbridge.OpsAgencyCommand) (map[string]any, error) {
	target := strings.TrimSpace(fmt.Sprint(command.Payload["targetMode"]))
	reason := strings.TrimSpace(fmt.Sprint(command.Payload["reason"]))
	expected, err := strconv.ParseInt(strings.TrimSpace(fmt.Sprint(command.Payload["expectedFenceVersion"])), 10, 64)
	if err != nil || expected < 1 {
		return nil, errors.New("expected writer fence version is required")
	}
	evidence, ok := command.Payload["evidence"].(map[string]any)
	if !ok || len(evidence) == 0 {
		return nil, errors.New("writer transition evidence is required")
	}
	health := opsbridge.DeliveryHealth{}
	if opsbridge.Default != nil {
		health = opsbridge.Default.DeliveryHealth()
	}
	state, err := model.TransitionAgencyOpsWriter(ctx, model.DB, command.ID, target, expected, reason, evidence, model.AgencyOpsDeliveryHealth{
		Backlog: health.Backlog, NeedsRepair: health.NeedsRepair,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"writerMode": state.Mode, "fenceVersion": state.FenceVersion, "manifestVersion": state.ManifestVersion}, nil
}

func opsCommandIDs(command opsbridge.OpsAgencyCommand) (int64, int64, error) {
	userID, err := strconv.ParseInt(strings.TrimSpace(fmt.Sprint(command.Payload["externalUserId"])), 10, 64)
	if err != nil || userID <= 0 {
		return 0, 0, errors.New("external user mapping is missing or invalid")
	}
	agencyID, err := strconv.ParseInt(strings.TrimSpace(fmt.Sprint(command.Payload["externalAgencyId"])), 10, 64)
	if err != nil || agencyID <= 0 {
		return 0, 0, errors.New("external agency mapping is missing or invalid")
	}
	return userID, agencyID, nil
}

func executeOpsProvisionCustomer(ctx context.Context, command opsbridge.OpsAgencyCommand) (map[string]any, error) {
	userID, agencyID, err := opsCommandIDs(command)
	if err != nil {
		return nil, err
	}
	var binding model.AgencyUserBinding
	err = model.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var agency model.Agency
		if err := model.AgencyLockForUpdate(tx).First(&agency, agencyID).Error; err != nil {
			return err
		}
		if agency.Status != agencyhub.AgencyStatusActive {
			return errors.New("target agency is disabled")
		}
		var active model.AgencyActiveUserBinding
		if err := tx.Where("user_id = ?", userID).First(&active).Error; err == nil {
			if active.AgencyID != agencyID {
				return errors.New("user is already bound to another agency")
			}
			return tx.First(&binding, active.BindingID).Error
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		// Existing wallets require the gateway's fenced Root provisioning job.
		// A tenant-scoped bridge signature is not a substitute for Root proof.
		return errors.New("gateway_root_provisioning_required")
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"userId": userID, "agencyId": agencyID, "bindingId": binding.ID, "created": false}, nil
}

func executeOpsCustomerStatus(ctx context.Context, command opsbridge.OpsAgencyCommand, status int) (map[string]any, error) {
	userID, agencyID, err := opsCommandIDs(command)
	if err != nil {
		return nil, err
	}
	changed := false
	err = model.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var agency model.Agency
		if err := model.AgencyLockForUpdate(tx).First(&agency, agencyID).Error; err != nil {
			return err
		}
		if agency.Status != agencyhub.AgencyStatusActive {
			return errors.New("target agency is disabled")
		}
		var binding model.AgencyActiveUserBinding
		if err := model.AgencyLockForUpdate(tx).Where("user_id = ?", userID).First(&binding).Error; err != nil {
			return err
		}
		if binding.AgencyID != agencyID {
			return errors.New("user agency binding does not match the command")
		}
		var user model.User
		if err := model.AgencyLockForUpdate(tx).First(&user, userID).Error; err != nil {
			return err
		}
		if user.Role != common.RoleCommonUser {
			return errors.New("only customer accounts can be suspended or resumed")
		}
		if user.Status == status {
			return nil
		}
		before := user.Status
		user.Status = status
		if err := user.UpdateWithTx(tx, false); err != nil {
			return err
		}
		changed = true
		return tx.Create(&model.AgencyAuditLog{EventID: "audit-" + common.NewRequestId(), ActorType: "tokenops", ActorID: 0,
			ActingAgencyID: &agencyID, Action: "customer.status.tokenops", ObjectType: "user", ObjectID: strconv.FormatInt(userID, 10),
			RequestID: command.ID, Reason: "TokenOps command", BeforeJSON: fmt.Sprintf(`{"status":%d}`, before),
			AfterJSON: fmt.Sprintf(`{"status":%d}`, status), CreatedAtMS: time.Now().UnixMilli()}).Error
	})
	if err != nil {
		return nil, err
	}
	if err := model.InvalidateUserCache(int(userID)); err != nil {
		return nil, err
	}
	if err := model.InvalidateUserTokensCache(int(userID)); err != nil {
		return nil, err
	}
	if _, err := model.RevokeAllUserSessions(int(userID), "tokenops_customer_status_changed"); err != nil {
		return nil, err
	}
	return map[string]any{"userId": userID, "agencyId": agencyID, "status": status, "changed": changed}, nil
}

func executeOpsAgencyPolicy(ctx context.Context, command opsbridge.OpsAgencyCommand) (map[string]any, error) {
	agencyID, err := strconv.ParseInt(strings.TrimSpace(fmt.Sprint(command.Payload["externalAgencyId"])), 10, 64)
	if err != nil || agencyID <= 0 {
		return nil, errors.New("external agency mapping is missing or invalid")
	}
	version, ok := command.Payload["version"].(map[string]any)
	if !ok {
		return nil, errors.New("TokenOps pricing version is missing")
	}
	effectiveDate := strings.TrimSpace(fmt.Sprint(version["effective_from"]))
	if effectiveDate == "<nil>" {
		effectiveDate = strings.TrimSpace(fmt.Sprint(version["effectiveFrom"]))
	}
	if _, err := time.Parse("2006-01-02", effectiveDate); err != nil || effectiveDate > time.Now().UTC().Format("2006-01-02") {
		return nil, errors.New("TokenOps pricing version is not yet effective")
	}
	rawRules, ok := version["rules"].([]any)
	if !ok || len(rawRules) == 0 {
		return nil, errors.New("TokenOps pricing rules are missing")
	}
	policy, customers, children, err := mapOpsAgencyPolicy(rawRules)
	if err != nil {
		return nil, err
	}
	var revision int64
	var created bool
	err = model.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var writer model.AgencyOpsWriterState
		if err := model.AgencyLockForUpdate(tx).First(&writer, 1).Error; err != nil {
			return fmt.Errorf("agency writer fence unavailable: %w", err)
		}
		if writer.Mode != model.AgencyWriterOpsSingle {
			return errors.New("TokenOps pricing requires the Ops single-writer mode")
		}
		var applyErr error
		revision, created, applyErr = agencyhub.ApplyOpsAgencyPolicy(tx, agencyID, policy, customers, children, command.ID)
		return applyErr
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"agencyId": agencyID, "revision": revision, "published": created}, nil
}

func mapOpsAgencyPolicy(rawRules []any) (agencycontract.Policy, []agencyhub.OpsCustomerSalesOverride, []agencyhub.OpsChildPolicyOverride, error) {
	var policy agencycontract.Policy
	var customers []agencyhub.OpsCustomerSalesOverride
	var children []agencyhub.OpsChildPolicyOverride
	baseFound := false
	// Read the default first; model/customer validation must not depend on order.
	ordered := make([]any, 0, len(rawRules))
	for _, value := range rawRules {
		if rule, ok := value.(map[string]any); ok && fmt.Sprint(firstValue(rule, "model_name", "modelName")) == "*" && firstValue(rule, "customer_id", "customerId") == nil && firstValue(rule, "child_distributor_id", "childAgencyId") == nil {
			ordered = append(ordered, value)
		}
	}
	if len(ordered) != 1 {
		return policy, nil, nil, errors.New("TokenOps pricing requires exactly one wildcard default rule")
	}
	for _, value := range rawRules {
		if rule, ok := value.(map[string]any); ok && fmt.Sprint(firstValue(rule, "model_name", "modelName")) == "*" && firstValue(rule, "customer_id", "customerId") == nil && firstValue(rule, "child_distributor_id", "childAgencyId") == nil {
			continue
		}
		if rule, ok := value.(map[string]any); ok && firstValue(rule, "customer_id", "customerId") != nil {
			continue
		}
		ordered = append(ordered, value)
	}
	for _, value := range rawRules {
		if rule, ok := value.(map[string]any); ok && firstValue(rule, "customer_id", "customerId") != nil {
			ordered = append(ordered, value)
		}
	}
	for _, value := range ordered {
		rule, ok := value.(map[string]any)
		if !ok {
			return policy, nil, nil, errors.New("invalid TokenOps pricing rule")
		}
		modelValue := firstValue(rule, "model_name", "modelName")
		if modelValue == nil {
			return policy, nil, nil, errors.New("TokenOps pricing model is missing")
		}
		modelName, validName := modelValue.(string)
		if !validName || strings.TrimSpace(modelName) != modelName || modelName == "" {
			return policy, nil, nil, errors.New("TokenOps pricing model is missing")
		}
		customer := firstValue(rule, "customer_id", "customerId")
		child := firstValue(rule, "child_distributor_id", "childAgencyId")
		cost, costSet, err := opsMultiplier(rule, "cost_multiplier", "costMultiplier")
		if err != nil {
			return policy, nil, nil, err
		}
		childCost, childCostSet, err := opsMultiplier(rule, "child_cost_multiplier", "childCostMultiplier")
		if err != nil {
			return policy, nil, nil, err
		}
		sale, saleSet, err := opsMultiplier(rule, "sale_multiplier", "saleMultiplier")
		if err != nil {
			return policy, nil, nil, err
		}
		spread, spreadSet, err := opsMultiplier(rule, "min_spread_multiplier", "minSpreadMultiplier")
		if err != nil {
			return policy, nil, nil, err
		}
		cap, capSet, err := opsMultiplier(rule, "max_sale_multiplier", "maxSaleMultiplier")
		if err != nil {
			return policy, nil, nil, err
		}
		if child != nil {
			if !childCostSet {
				childCost, childCostSet = cost, costSet
			}
			if !childCostSet {
				return policy, nil, nil, errors.New("child pricing requires a child cost multiplier")
			}
			childID, parseErr := strconv.ParseInt(strings.TrimSpace(fmt.Sprint(firstValue(rule, "externalChildAgencyId"))), 10, 64)
			if parseErr != nil || childID <= 0 {
				return policy, nil, nil, errors.New("child pricing external agency mapping is missing")
			}
			override := agencyhub.OpsChildPolicyOverride{AgencyID: childID, OriginModelName: modelName, SettlementBPS: childCost}
			if saleSet {
				override.SalesBPS = &sale
			}
			children = append(children, override)
			continue
		}
		if customer != nil {
			resolved, resolveErr := agencycontract.Resolve(policy, modelName)
			if modelName == "*" {
				resolved.SettlementBPS = policy.DefaultSettlementBPS
			}
			if resolveErr != nil || (costSet && cost != resolved.SettlementBPS) || (spreadSet && spread != policy.MinSpreadBPS) || (capSet && cap != policy.SalesCapBPS) {
				return policy, nil, nil, errors.New("customer pricing contains unsupported cost, spread or cap overrides")
			}
			if !saleSet {
				return policy, nil, nil, errors.New("customer pricing requires a sales multiplier")
			}
			userID, parseErr := strconv.ParseInt(strings.TrimSpace(fmt.Sprint(rule["externalUserId"])), 10, 64)
			if parseErr != nil || userID <= 0 {
				return policy, nil, nil, errors.New("customer pricing external user mapping is missing")
			}
			customers = append(customers, agencyhub.OpsCustomerSalesOverride{UserID: userID, OriginModelName: modelName, SalesBPS: sale})
			continue
		}
		if modelName == "*" {
			if !costSet || !saleSet {
				return policy, nil, nil, errors.New("TokenOps default pricing requires cost and sales multipliers")
			}
			policy.DefaultSettlementBPS = cost
			if childCostSet {
				policy.DefaultChildCostBPS = childCost
			} else {
				policy.DefaultChildCostBPS = sale
			}
			policy.DefaultSalesBPS = sale
			policy.MinSpreadBPS = spread
			if capSet {
				policy.SalesCapBPS = cap
			}
			if policy.SalesCapBPS == 0 {
				policy.SalesCapBPS = agencycontract.DefaultSalesCapBPS
			}
			baseFound = true
			continue
		}
		override := agencycontract.ModelOverride{OriginModelName: modelName}
		if costSet {
			override.SettlementBPS = &cost
		}
		if saleSet {
			override.SalesBPS = &sale
		}
		if childCostSet {
			override.ChildCostBPS = &childCost
		} else if saleSet {
			override.ChildCostBPS = &sale
		}
		if spreadSet && spread != policy.MinSpreadBPS {
			return policy, nil, nil, errors.New("per-model TokenOps minimum spread cannot be represented by the current gateway policy contract")
		}
		if capSet && cap != policy.SalesCapBPS {
			return policy, nil, nil, errors.New("per-model TokenOps sales cap cannot be represented by the current gateway policy contract")
		}
		policy.ModelOverrides = append(policy.ModelOverrides, override)
	}
	if !baseFound {
		return policy, nil, nil, errors.New("TokenOps pricing requires a wildcard default rule")
	}
	return policy, customers, children, nil
}

func firstValue(values map[string]any, names ...string) any {
	for _, name := range names {
		if value, exists := values[name]; exists && value != nil && fmt.Sprint(value) != "" {
			return value
		}
	}
	return nil
}

func opsMultiplier(values map[string]any, names ...string) (int, bool, error) {
	value := firstValue(values, names...)
	if value == nil {
		return 0, false, nil
	}
	ratio, ok := new(big.Rat).SetString(strings.TrimSpace(fmt.Sprint(value)))
	if !ok || ratio.Sign() < 0 {
		return 0, false, errors.New("invalid TokenOps pricing multiplier")
	}
	ratio.Mul(ratio, big.NewRat(10000, 1))
	if !ratio.IsInt() || !ratio.Num().IsInt64() || ratio.Num().Int64() > agencycontract.MaxCoefficientBPS {
		return 0, false, errors.New("TokenOps pricing multiplier exceeds gateway precision or range")
	}
	return int(ratio.Num().Int64()), true, nil
}
