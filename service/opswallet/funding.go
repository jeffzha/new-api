package opswallet

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
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
	if client == nil {
		return errors.New("OPS funding client missing")
	}
	// The worker deadline bounds the whole delivery, including connection-pool
	// waits and the local acknowledgement transaction after OPS accepts it.
	// An interrupted acknowledgement leaves the original batch for safe replay.
	db = db.WithContext(ctx)
	rows, err := model.ReadOPSWalletFunding(db, 50)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		return nil
	}
	target, err := model.OPSWalletFundingTarget(client.BaseURL, client.KeyID)
	if err != nil {
		return err
	}
	events := make([]json.RawMessage, 0, len(rows))
	for _, row := range rows {
		if err := model.ValidateOPSWalletFunding(row); err != nil {
			return err
		}
		var stored struct {
			Events []json.RawMessage `json:"events"`
		}
		if common.UnmarshalJsonStr(row.Payload, &stored) != nil || len(stored.Events) != 1 {
			return errors.New("invalid stored OPS funding batch")
		}
		var event struct {
			Extra struct {
				Binding struct {
					Target string `json:"target"`
				} `json:"walletFunding"`
			} `json:"extra"`
		}
		if common.Unmarshal(stored.Events[0], &event) != nil || event.Extra.Binding.Target != target {
			return errors.New("OPS funding destination changed; pending events require reconciliation")
		}
		events = append(events, stored.Events[0])
	}
	// Preserve integer quota and frozen per-event bytes when batching; decoding
	// through map[string]any would round large quotas through float64.
	eventBytes, err := common.Marshal(events)
	if err != nil {
		return err
	}
	body, err := common.Marshal(map[string]any{"schemaVersion": "1.1", "batchId": fmt.Sprintf("wf-batch-%x", sha256.Sum256(eventBytes)), "events": events})
	if err != nil {
		return err
	}
	if len(body) > 1024*1024 {
		return errors.New("OPS funding batch exceeds limit")
	}
	if err := opsclient.ReserveIngest(ctx, client.BaseURL, client.KeyID, opsclient.IngestFunding); err != nil {
		return err
	}
	raw, err := client.Post(ctx, fundingPath, body, time.Now())
	if err != nil {
		var remote *opsclient.ReadError
		if errors.As(err, &remote) && remote.Status == 429 {
			opsclient.CoolIngest(ctx, client.BaseURL, client.KeyID, remote.RetryAfter)
		}
		return err
	}
	var receipt struct {
		Data *struct {
			Accepted   *int  `json:"accepted"`
			Duplicates *int  `json:"duplicates"`
			Rejected   []any `json:"rejected"`
		} `json:"data"`
	}
	if common.Unmarshal(raw, &receipt) != nil || receipt.Data == nil || receipt.Data.Accepted == nil || receipt.Data.Duplicates == nil || *receipt.Data.Accepted < 0 || *receipt.Data.Duplicates < 0 || *receipt.Data.Accepted+*receipt.Data.Duplicates != len(rows) || len(receipt.Data.Rejected) != 0 {
		return errors.New("OPS funding acknowledgement incomplete; retained original events")
	}
	return db.Transaction(func(tx *gorm.DB) error {
		for _, row := range rows {
			if err := model.AcknowledgeOPSWalletFunding(tx, row, time.Now().Unix()); err != nil {
				return err
			}
		}
		return nil
	})
}
