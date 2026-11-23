package opsfees

import (
	"context"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

// AcceptPushReceipts is called only after the signed usage request receives a
// complete ACK. Canonical logFee values are display results, never wallet debits.
func AcceptPushReceipts(base, key, secret string, sent, response []byte, at time.Time) error {
	c := receiptConfig.Load()
	if c == nil || base != c.BaseURL || key != c.KeyID || secret != c.Secret {
		return nil
	}
	var batch struct {
		Events []struct {
			RequestID string `json:"requestId"`
			UserID    string `json:"userId"`
			Model     string `json:"model"`
			Currency  string `json:"currency"`
		} `json:"events"`
	}
	var ack struct {
		Data struct {
			Currency string `json:"currency"`
			Charges  []struct {
				RequestID string `json:"requestId"`
				UserID    string `json:"userId"`
				Model     string `json:"model"`
				Currency  string `json:"currency"`
				Status    string `json:"status"`
				LogFee    *Fee   `json:"logFee"`
			} `json:"charges"`
		} `json:"data"`
	}
	if common.Unmarshal(sent, &batch) != nil || common.Unmarshal(response, &ack) != nil || len(batch.Events) > 500 || len(ack.Data.Charges) > 500 {
		return nil
	}
	currency := c.Currency
	if currency == "" {
		currency = "CNY"
	}
	if ack.Data.Currency != "" && ack.Data.Currency != currency {
		return nil
	}
	identities := map[string]string{}
	for _, e := range batch.Events {
		actual := e.Currency
		if actual == "" {
			actual = "CNY"
		}
		if actual != currency {
			continue
		}
		identities[e.RequestID] = e.UserID + "\n" + e.Model
	}
	items := []Fee{}
	for _, charge := range ack.Data.Charges {
		f := charge.LogFee
		if charge.Status != "charged" && charge.Status != "station_billed" || charge.Currency != "" && charge.Currency != currency {
			continue
		}
		if f == nil || f.Amount == nil || f.RequestID != charge.RequestID || f.UserID != charge.UserID || charge.Model != "" && charge.Model != f.Model || identities[f.RequestID] != f.UserID+"\n"+f.Model {
			continue
		}
		if user, err := strconv.Atoi(f.UserID); err != nil || user <= 0 {
			continue
		}
		items = append(items, *f)
	}
	if len(items) == 0 {
		return nil
	}
	raw, err := common.Marshal(map[string]any{"data": map[string]any{"currency": currency, "items": items, "nextCursor": "receipt", "hasMore": false}})
	if err != nil {
		return nil
	}
	rows, _, _, err := decodePage(raw, c.Connection, at.Unix(), currency)
	if err != nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return model.SaveOpsFeeReceipts(ctx, model.DB, c.Connection, rows)
}
