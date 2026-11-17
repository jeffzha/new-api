package service

import (
	"context"
	"errors"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/agencycontract"
	"github.com/QuantumNous/new-api/service/opsbridge"
	"gorm.io/gorm"
)

type OpsBillingEventSource struct{}

var opsAuthorityEventKinds = map[string]bool{
	"agency.billing_reserved":  true,
	"agency.billing_finalized": true,
	"agency.billing_reversed":  true,
	"agency.billing_corrected": true,
	"agency.topup_completed":   true,
	"agency.funding_adjusted":  true,
	"agency.funding_reversed":  true,
	"agency.wallet_checkpoint": true,
}

func (OpsBillingEventSource) NextOpsBillingEvent(ctx context.Context, after int64) (*opsbridge.BillingEvent, error) {
	if after < 0 {
		return nil, errors.New("invalid Ops billing cursor")
	}
	if err := model.PublishAgencyOpsEvents(ctx, model.DB); err != nil {
		return nil, err
	}
	var publication model.AgencyOpsPublication
	err := model.DB.WithContext(ctx).Where("id > ?", after).Order("id ASC").First(&publication).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var row model.AgencyBillingOutbox
	if err := model.DB.WithContext(ctx).Where("id = ?", publication.SourceID).First(&row).Error; err != nil {
		return nil, err
	}
	if row.EventID != publication.EventID || row.PayloadHash != publication.PayloadHash {
		return nil, errors.New("Ops authority publication identity mismatch")
	}
	if !opsAuthorityEventKinds[row.EventKind] || row.PayloadHash == "" || len(row.Payload) > 1<<20 {
		return nil, errors.New("unsupported or invalid Ops authority event")
	}
	var contract agencycontract.BillingEvent
	if common.UnmarshalJsonStr(row.Payload, &contract) != nil || contract.EventID != row.EventID ||
		contract.EventType != row.EventKind || contract.OperationID != row.OperationID || contract.UserID != row.UserID ||
		contract.MoneySeq != row.MoneySeq || contract.EventIndex != row.EventIndex || contract.EventCount != row.EventCount {
		return nil, errors.New("Ops authority event identity mismatch")
	}
	digest, err := agencycontract.CanonicalHash(&contract)
	if err != nil || digest != row.PayloadHash {
		return nil, errors.New("Ops authority event hash mismatch")
	}
	proof := opsbridge.BillingSourceProof(row.Payload)
	return &opsbridge.BillingEvent{Cursor: publication.ID, PayloadHash: row.PayloadHash, SourceProof: proof, RawPayload: row.Payload}, nil
}

func (OpsBillingEventSource) NextOpsSourceManifest(ctx context.Context, after int64, health opsbridge.DeliveryHealth) (*opsbridge.SourceManifest, error) {
	row, err := model.CreateAgencyOpsSourceManifest(ctx, model.DB, after, model.AgencyOpsDeliveryHealth{
		Backlog: health.Backlog, NeedsRepair: health.NeedsRepair,
	})
	if err != nil || row == nil {
		return nil, err
	}
	return &opsbridge.SourceManifest{Version: row.Version, PayloadHash: row.PayloadHash, RawPayload: row.Payload}, nil
}
