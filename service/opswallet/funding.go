package opswallet

import (
	"context"
	"errors"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service/opsclient"
	"gorm.io/gorm"
)

const fundingPath = "/api/v1/ingest/v1/usage-events"

// DeliverFunding sends a bounded set of committed funding events. Never
// acknowledge partial acceptance or a malformed receipt, including HTTP 200.
func DeliverFunding(ctx context.Context, db *gorm.DB, client *opsclient.Client) error {
	rows, err := model.ReadOPSUSDFunding(db, 20)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if err := model.ValidateOPSUSDFunding(row); err != nil {
			return err
		}
		raw, err := client.Post(ctx, fundingPath, []byte(row.Payload), time.Now())
		if err != nil {
			return err
		}
		var receipt struct {
			Data *struct {
				Accepted   *int  `json:"accepted"`
				Duplicates *int  `json:"duplicates"`
				Rejected   []any `json:"rejected"`
			} `json:"data"`
		}
		if common.Unmarshal(raw, &receipt) != nil || receipt.Data == nil || receipt.Data.Accepted == nil || receipt.Data.Duplicates == nil || *receipt.Data.Accepted < 0 || *receipt.Data.Duplicates < 0 || *receipt.Data.Accepted+*receipt.Data.Duplicates != 1 || len(receipt.Data.Rejected) != 0 {
			return errors.New("OPS funding acknowledgement incomplete; retained original event")
		}
		if err := model.AcknowledgeOPSUSDFunding(db, row, time.Now().Unix()); err != nil {
			return err
		}
	}
	return nil
}
