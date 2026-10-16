package model

import (
	"context"
	"errors"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

type CNDependency struct {
	SourceID         string   `json:"sourceId"`
	Version          string   `json:"version"`
	Required         bool     `json:"required"`
	CoverageFrom     string   `json:"coverageFrom"`
	CoverageTo       *string  `json:"coverageTo"`
	SourceCutoff     *string  `json:"sourceCutoff"`
	CapturedThrough  *string  `json:"capturedThrough"`
	PublishedThrough *string  `json:"publishedThrough"`
	PendingCount     int64    `json:"pendingCount"`
	UnknownCount     int64    `json:"unknownCount"`
	SupportedPaths   []string `json:"supportedPaths"`
	ExcludedPaths    []string `json:"excludedPaths"`
	ProofID          *string  `json:"proofId"`
	Status           string   `json:"status"`
}

func cnPublishCheckpoint(tx *gorm.DB, stream *CNFinanceStream) error {
	payload, err := cnBuildSourceProofs(tx, stream)
	if err != nil {
		return err
	}
	var last CNFinancePublication
	err = tx.Where("fact_id IS NULL").Order("sequence DESC").First(&last).Error
	if err == nil && last.Payload == string(payload) {
		return nil
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}
	return cnPublishRecord(tx, stream, nil, string(payload))
}

type cnRawControl struct {
	CumulativeGross   map[string]string `json:"cumulativeGrossByCurrency"`
	CumulativeQuota   string            `json:"cumulativeQuota"`
	DeltaQuota        string            `json:"deltaQuota"`
	UnknownMoneyCount int64             `json:"unknownMoneyCount"`
}

type cnEconomicControl struct {
	DeltaGross      map[string]string `json:"deltaGrossByCurrency"`
	DeltaNet        map[string]string `json:"deltaNetByCurrency"`
	DeltaTax        map[string]string `json:"deltaTaxByCurrency"`
	DeltaQuota      string            `json:"deltaQuota"`
	RecognizedGross map[string]string `json:"recognizedGrossByCurrency"`
}

func cnAdd(target map[string]string, currency, value string) {
	a, _ := decimal.NewFromString(target[currency])
	b, _ := decimal.NewFromString(value)
	target[currency] = a.Add(b).String()
}

func ReadCNDaily(ctx context.Context, db *gorm.DB, day, through string) (map[string]any, error) {
	parsed, err := time.Parse("2006-01-02", day)
	if err != nil || parsed.Year() < 1 || parsed.Format("2006-01-02") != day {
		return nil, errors.New("invalid date")
	}
	var result map[string]any
	err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		stream, err := cnLockPublisher(tx)
		if err != nil {
			return err
		}
		n, err := ParseCNCursor(through, stream)
		if err != nil {
			return err
		}
		if through == "" {
			if err = cnCaptureSources(tx); err != nil {
				return err
			}
			if err = cnPublishPending(tx, &stream); err != nil {
				return err
			}
			if err = cnPublishCheckpoint(tx, &stream); err != nil {
				return err
			}
			n = stream.Sequence
		}
		raw := map[string]*cnRawControl{}
		economic := map[string]*cnEconomicControl{}
		counts := map[string]int64{}
		var count, unknown, after int64
		for {
			var rows []CNFinancePublication
			if err = tx.Where("day = ? AND sequence > ? AND sequence <= ?", day, after, n).Order("sequence").Limit(500).Find(&rows).Error; err != nil {
				return err
			}
			for _, row := range rows {
				var f CNFact
				if err = common.UnmarshalJsonStr(row.Payload, &f); err != nil {
					return err
				}
				count++
				counts[f.Kind]++
				if raw[f.Kind] == nil {
					raw[f.Kind] = &cnRawControl{CumulativeGross: map[string]string{}, CumulativeQuota: "0", DeltaQuota: "0"}
					economic[f.Kind] = &cnEconomicControl{DeltaGross: map[string]string{}, DeltaNet: map[string]string{}, DeltaTax: map[string]string{}, DeltaQuota: "0", RecognizedGross: map[string]string{}}
				}
				r, e := raw[f.Kind], economic[f.Kind]
				if f.Cumulative.Quota != nil {
					a, _ := decimal.NewFromString(r.CumulativeQuota)
					b, _ := decimal.NewFromString(*f.Cumulative.Quota)
					r.CumulativeQuota = a.Add(b).String()
					a, _ = decimal.NewFromString(r.DeltaQuota)
					b, _ = decimal.NewFromString(*f.Delta.Quota)
					r.DeltaQuota = a.Add(b).String()
					e.DeltaQuota = r.DeltaQuota
				}
				if f.Cumulative.Money == nil {
					r.UnknownMoneyCount++
					unknown++
				} else {
					m, d := f.Cumulative.Money, f.Delta.Money
					cnAdd(r.CumulativeGross, m.Currency, m.Gross)
					cnAdd(e.DeltaGross, d.Currency, d.Gross)
					if d.Net != nil {
						cnAdd(e.DeltaNet, d.Currency, *d.Net)
						cnAdd(e.DeltaTax, d.Currency, *d.Tax)
					} else {
						unknown++
					}
				}
				if f.Recognition != nil {
					encoded, err := common.Marshal(f.Recognition["delta"])
					if err != nil {
						return err
					}
					var m CNMoney
					if err = common.Unmarshal(encoded, &m); err != nil {
						return err
					}
					cnAdd(e.RecognizedGross, m.Currency, m.Gross)
				} else if f.Kind == "charge" {
					unknown++
				}
				after = row.Sequence
			}
			if len(rows) < 500 {
				break
			}
		}
		deps := []CNDependency{}
		gaps := []string{}
		var checkpoint CNFinancePublication
		var backfillComplete, sourceComplete, forwardComplete bool
		var unresolved, quarantined int64
		err = tx.Where("fact_id IS NULL AND sequence <= ?", n).Order("sequence DESC").First(&checkpoint).Error
		if err == nil {
			var proof struct {
				Dependencies           []CNDependency `json:"dependencies"`
				BackfillComplete       bool           `json:"backfillComplete"`
				SourceCoverageComplete bool           `json:"sourceCoverageComplete"`
				ForwardComplete        bool           `json:"forwardGatewayScopeComplete"`
				Unresolved             int64          `json:"unresolvedPredecessorCount"`
				Quarantined            int64          `json:"quarantinedCount"`
			}
			if err = common.UnmarshalJsonStr(checkpoint.Payload, &proof); err != nil {
				return err
			}
			deps = proof.Dependencies
			backfillComplete, sourceComplete, forwardComplete = proof.BackfillComplete, proof.SourceCoverageComplete, proof.ForwardComplete
			unresolved, quarantined = proof.Unresolved, proof.Quarantined
			for _, d := range deps {
				if d.Status != "complete" && d.Status != "not_applicable" {
					gaps = append(gaps, d.SourceID)
				}
			}
		} else if errors.Is(err, gorm.ErrRecordNotFound) {
			gaps = append(gaps, "no_dependency_checkpoint_at_watermark")
		} else {
			return err
		}
		result = map[string]any{"schemaVersion": "2", "gatewayId": stream.GatewayID, "sourceSystem": CNFinanceSource, "streamEpoch": stream.Epoch, "date": day, "timezone": "UTC", "through": CNCursor(stream.Epoch, n), "rawVersionControls": map[string]any{"count": count, "countByKind": counts, "byKind": raw}, "economicNetControls": map[string]any{"byKind": economic, "unresolvedPredecessorCount": unresolved, "unresolvedEvidenceCount": unknown}, "backfillComplete": backfillComplete, "sourceCoverageComplete": sourceComplete, "forwardGatewayScopeComplete": forwardComplete, "quarantinedCount": quarantined, "dependencies": deps, "coverageGaps": gaps}
		return nil
	})
	return result, err
}
