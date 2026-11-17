package opsfees

import (
	"encoding/json"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

var receiptConfig atomic.Pointer[Config]

type pushPublication struct{ Send func([]byte, []byte) }

var sharePush atomic.Pointer[pushPublication]
var receiptObserver atomic.Pointer[func([]byte, []byte, time.Time)]

// Project startup supplies its actual-debit receipt consumer. Product leaves
// it unset; neither the common fee reader nor this hook changes wallet mode.
func ConfigureReceiptObserver(observer func([]byte, []byte, time.Time)) {
	if observer == nil {
		receiptObserver.Store(nil)
		return
	}
	receiptObserver.Store(&observer)
}

// AcceptPushReceipts consumes only the authenticated acknowledgement of the
// exact usage batch. It performs no network or SQL work and changes no wallet.
func AcceptPushReceipts(base, key, secret string, sent, response []byte, at time.Time) {
	c := receiptConfig.Load()
	if c == nil || base != c.BaseURL || !(key == c.KeyID && secret == c.Secret || c.ReceiptKeyID != "" && key == c.ReceiptKeyID) {
		return
	}
	var batch struct {
		Events []struct {
			RequestID string `json:"requestId"`
			UserID    string `json:"userId"`
			Model     string `json:"model"`
		} `json:"events"`
	}
	var ack struct {
		Data struct {
			Charges []struct {
				RequestID string `json:"requestId"`
				UserID    string `json:"userId"`
				LogFee    *Fee   `json:"logFee"`
			} `json:"charges"`
		} `json:"data"`
	}
	if common.Unmarshal(sent, &batch) != nil || common.Unmarshal(response, &ack) != nil || len(batch.Events) > 500 || len(ack.Data.Charges) > 500 {
		return
	}
	if observer := receiptObserver.Load(); observer != nil {
		(*observer)(sent, response, at)
	}
	if publication := sharePush.Load(); publication != nil {
		// The sent identities are enough for the existing charge validator.
		identities, _ := common.Marshal(batch)
		var envelope struct {
			Data struct {
				Charges []map[string]json.RawMessage `json:"charges"`
			} `json:"data"`
		}
		if common.Unmarshal(response, &envelope) == nil {
			for _, charge := range envelope.Data.Charges {
				for key := range charge {
					switch key {
					case "requestId", "userId", "model", "status", "reason", "amount", "refundAmount", "netAmount", "stationAmount", "chargedAt", "logFee":
					default:
						delete(charge, key)
					}
				}
				if raw, ok := charge["logFee"]; ok {
					var fee Fee
					if common.Unmarshal(raw, &fee) == nil {
						charge["logFee"], _ = common.Marshal(fee)
					} else {
						delete(charge, "logFee")
					}
				}
			}
			clean, err := common.Marshal(envelope)
			if err == nil {
				publication.Send(identities, clean)
			}
		}
	}
	identities := map[string]string{}
	for _, e := range batch.Events {
		identities[e.RequestID] = e.UserID + "\n" + e.Model
	}
	items := []Fee{}
	for _, charge := range ack.Data.Charges {
		f := charge.LogFee
		if f == nil || f.RequestID != charge.RequestID || f.UserID != charge.UserID || identities[f.RequestID] != f.UserID+"\n"+f.Model || f.Amount == nil {
			continue
		}
		if user, err := strconv.Atoi(f.UserID); err != nil || user <= 0 {
			continue
		}
		items = append(items, *f)
	}
	if len(items) == 0 {
		return
	}
	// Reuse the exact feed decoder for amount, timestamp and identity checks.
	raw, err := common.Marshal(map[string]any{"data": map[string]any{"items": items, "nextCursor": "receipt", "hasMore": false}})
	if err != nil {
		return
	}
	rows, _, _, err := decodePage(raw, c.Connection, at.Unix())
	if err == nil {
		model.AcceptOpsFeeResults(c.Connection, rows, false)
	}
}
