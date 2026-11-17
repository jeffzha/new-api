// agency-price-bridge is a server-local, bounded operator transport. It has no
// HTTP listener and uses the Hub container's existing regional database config.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/QuantumNous/new-api/pkg/agencyhub"
	"github.com/QuantumNous/new-api/service/agencyopspush"
	"gorm.io/gorm"
)

type request struct {
	Action           string                               `json:"action"`
	AgencyID         int64                                `json:"agencyId"`
	AgencyIDs        []int64                              `json:"agencyIds"`
	ExpectedSnapshot string                               `json:"expectedSnapshot"`
	CommandID        string                               `json:"commandId"`
	Policy           agencycontract.Policy                `json:"policy"`
	Customers        []agencyhub.OpsCustomerSalesOverride `json:"customers"`
	Children         []agencyhub.OpsChildPolicyOverride   `json:"children"`
	Name             string                               `json:"name"`
	Status           string                               `json:"status"`
	AfterUserID      int64                                `json:"afterUserId"`
	UserID           int64                                `json:"userId"`
	BindingRevision  int64                                `json:"bindingRevision"`
	Account          agencyhub.OpsPayoutAccount           `json:"account"`
	RootUserID       int64                                `json:"rootUserId"`
	Distributor      agencyhub.OpsDistributorProvision    `json:"distributor"`
	Credential       agencyhub.OpsCredentialChange        `json:"credential"`
	ReviewBeforeMS   int64                                `json:"reviewBeforeMs"`
	ReviewAtMS       int64                                `json:"reviewAtMs"`
	Reviews          []model.AgencyOpsReview              `json:"reviews"`
	Withdrawal       agencyhub.OpsWithdrawalCommand       `json:"withdrawal"`
	IssueID          int64                                `json:"issueId"`
	BeforeID         int64                                `json:"beforeId"`
	PlatformPrice    agencyhub.OpsPlatformPriceCommand    `json:"platformPrice"`
	WithdrawalAction agencyhub.OpsWithdrawalAction        `json:"withdrawalAction"`
	Identity         agencyhub.OpsIdentityCommand         `json:"identity"`
}

func main() {
	if err := run(); err != nil {
		// Do not log database errors, DSNs, or payloads to the caller.
		fmt.Fprintln(os.Stderr, "agency price bridge failed")
		for _, prefix := range []string{"invalid agency pricing", "agency pricing revision conflict", "TokenOps ", "source price ", "customer ", "child ", "model ", "default ", "agency is disabled"} {
			if strings.HasPrefix(err.Error(), prefix) {
				fmt.Fprintln(os.Stderr, err.Error())
				break
			}
		}
		os.Exit(1)
	}
}

func run() error {
	var input request
	if err := common.DecodeJson(io.LimitReader(os.Stdin, 2<<20), &input); err != nil {
		return err
	}
	review := input.Action == "events-review" || input.Action == "events-review-ack" || input.Action == "withdrawal-create" || input.Action == "withdrawal-action" || input.Action == "identity" || input.Action == "reconciliation-read" || input.Action == "platform-read" || input.Action == "platform-apply"
	many := input.Action == "read-many" || input.Action == "directory-read" || input.Action == "customers-read" || input.Action == "payout-read" || input.Action == "distributor-provision" || input.Action == "credential-apply" || review
	if (input.AgencyID <= 0 && !many) || (!review && input.Action != "read" && input.Action != "read-many" && input.Action != "apply" && input.Action != "directory-read" && input.Action != "directory-apply" && input.Action != "customers-read" && input.Action != "customer-transfer" && input.Action != "payout-read" && input.Action != "payout-apply" && input.Action != "customer-provision" && input.Action != "distributor-provision" && input.Action != "credential-apply") || len(input.Customers) > 1000 || len(input.Children) > 1000 {
		return fmt.Errorf("invalid bounded bridge request")
	}
	if input.Action == "read-many" && (len(input.AgencyIDs) < 1 || len(input.AgencyIDs) > 5) {
		return fmt.Errorf("invalid bounded snapshot request")
	}
	if input.Action == "directory-read" && (len(input.AgencyIDs) < 1 || len(input.AgencyIDs) > 100) {
		return fmt.Errorf("invalid bounded directory request")
	}
	common.InitEnv()
	common.IsMasterNode = false
	if err := model.InitDB(); err != nil {
		return err
	}
	pool, err := model.DB.DB()
	if err != nil {
		return err
	}
	defer pool.Close()
	pool.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var result any
	if input.Action == "identity" {
		key, e := agencyhub.LoadSSOPublicKey(agencyhub.LoadConfig().SSOPublicKeyFile)
		if e != nil {
			return e
		}
		result, err = agencyhub.ApplyOpsIdentity(model.DB.WithContext(ctx), key, input.Identity)
	} else if input.Action == "platform-read" {
		err = model.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var e error
			result, e = agencyhub.ReadOpsPlatformPriceSnapshot(tx, input.RootUserID)
			return e
		}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	} else if input.Action == "platform-apply" {
		result, err = agencyhub.ApplyOpsPlatformPrice(model.DB.WithContext(ctx), input.PlatformPrice)
	} else if input.Action == "withdrawal-create" {
		result, err = agencyhub.ApplyOpsWithdrawal(model.DB.WithContext(ctx), input.Withdrawal, agencyhub.LoadConfig())
	} else if input.Action == "withdrawal-action" {
		result, err = agencyhub.ApplyOpsWithdrawalAction(model.DB.WithContext(ctx), input.WithdrawalAction, agencyhub.LoadConfig())
	} else if input.Action == "reconciliation-read" {
		err = model.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var e error
			result, e = agencyhub.ReadOpsReconciliation(tx, input.RootUserID, input.IssueID, input.BeforeID)
			return e
		}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	} else if input.Action == "events-review" {
		var events []model.AgencyOpsEvent
		events, err = model.ReadAgencyOpsReview(model.DB.WithContext(ctx), input.ReviewBeforeMS)
		items := make([]map[string]any, 0, len(events))
		for _, event := range events {
			items = append(items, map[string]any{"id": event.ID, "hash": event.PayloadHash, "payload": string(event.Payload)})
		}
		result = map[string]any{"items": items}
	} else if input.Action == "events-review-ack" {
		err = model.AcknowledgeAgencyOpsReview(model.DB.WithContext(ctx), input.Reviews, input.ReviewAtMS)
		result = map[string]any{"reviewed": len(input.Reviews)}
	} else if input.Action == "credential-apply" {
		result, err = agencyhub.ApplyOpsCredential(model.DB.WithContext(ctx), input.Credential)
	} else if input.Action == "distributor-provision" {
		result, err = agencyhub.ApplyOpsDistributorProvision(model.DB.WithContext(ctx), input.Distributor, agencyhub.LoadConfig())
	} else if input.Action == "customer-provision" {
		result, err = agencyhub.ApplyOpsCustomerProvision(model.DB.WithContext(ctx), input.UserID, input.AgencyID, input.RootUserID, input.CommandID)
	} else if input.Action == "payout-read" {
		err = model.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var e error
			result, e = agencyhub.ReadOpsPayoutSnapshots(tx, input.AgencyIDs)
			return e
		}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	} else if input.Action == "payout-apply" {
		result, err = agencyhub.ApplyOpsPayout(model.DB.WithContext(ctx), input.AgencyID, input.Account, input.Status, input.ExpectedSnapshot, input.CommandID)
	} else if input.Action == "customers-read" {
		result, err = agencyhub.ReadOpsCustomerBindings(model.DB.WithContext(ctx), input.AfterUserID, 100)
	} else if input.Action == "customer-transfer" {
		result, err = agencyhub.ApplyOpsCustomerTransfer(model.DB.WithContext(ctx), input.UserID,
			input.AgencyID, input.BindingRevision, input.CommandID)
	} else if input.Action == "directory-read" {
		err = model.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var e error
			result, e = agencyhub.ReadOpsDirectorySnapshots(tx, input.AgencyIDs)
			return e
		}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	} else if input.Action == "directory-apply" {
		result, err = agencyhub.ApplyOpsDirectory(model.DB.WithContext(ctx), input.AgencyID,
			input.Name, input.Status, input.ExpectedSnapshot, input.CommandID)
	} else if input.Action == "read" || input.Action == "read-many" {
		err = model.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if input.Action == "read-many" {
				items := []agencyhub.OpsPriceSnapshot{}
				for _, id := range input.AgencyIDs {
					if id <= 0 {
						return fmt.Errorf("invalid agency")
					}
					snapshot, e := agencyhub.ReadOpsPriceSnapshot(tx, id)
					if e != nil {
						return e
					}
					items = append(items, snapshot)
				}
				result = items
				return nil
			}
			var e error
			result, e = agencyhub.ReadOpsPriceSnapshot(tx, input.AgencyID)
			return e
		}, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	} else {
		if len(input.CommandID) < 8 || len(input.CommandID) > 160 {
			return fmt.Errorf("invalid command identity")
		}
		var revision int64
		var created bool
		revision, created, err = agencyhub.ApplyOpsAgencyPolicyAtSnapshot(model.DB.WithContext(ctx), input.AgencyID,
			input.Policy, input.Customers, input.Children, input.CommandID, input.ExpectedSnapshot)
		result = map[string]any{"revision": revision, "created": created}
	}
	if err != nil {
		return err
	}
	if input.Action == "apply" || input.Action == "directory-apply" || input.Action == "customer-transfer" ||
		input.Action == "customer-provision" || input.Action == "distributor-provision" ||
		input.Action == "payout-apply" || input.Action == "credential-apply" || input.Action == "withdrawal-create" || input.Action == "withdrawal-action" || input.Action == "platform-apply" {
		agencyopspush.NotifyConfiguredCommit()
	}
	encoded, err := common.Marshal(result)
	if err != nil {
		return err
	}
	fmt.Println("OPS_BRIDGE_RESULT:" + string(encoded))
	return nil
}
