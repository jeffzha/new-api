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
	if (input.AgencyID <= 0 && input.Action != "read-many") || (input.Action != "read" && input.Action != "read-many" && input.Action != "apply") || len(input.Customers) > 1000 || len(input.Children) > 1000 {
		return fmt.Errorf("invalid bounded bridge request")
	}
	if input.Action == "read-many" && (len(input.AgencyIDs) < 1 || len(input.AgencyIDs) > 5) {
		return fmt.Errorf("invalid bounded snapshot request")
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
	if input.Action == "read" || input.Action == "read-many" {
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
	encoded, err := common.Marshal(result)
	if err != nil {
		return err
	}
	fmt.Println("OPS_BRIDGE_RESULT:" + string(encoded))
	return nil
}
