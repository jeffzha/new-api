package seedancedomestic

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
)

const billPageSize = 1000

func (a *TaskAdaptor) ResolveTaskBilling(ctx context.Context, task *model.Task) (*service.TaskBillingResolution, error) {
	if task == nil || task.PrivateData.BillingContext == nil {
		return nil, fmt.Errorf("task billing context is missing")
	}
	snapshot := task.PrivateData.BillingContext.ProviderBilling
	if snapshot == nil || snapshot.Provider != providerName {
		return nil, fmt.Errorf("Seedance domestic billing snapshot is missing")
	}
	upstreamID, err := strconv.ParseInt(task.GetUpstreamTaskID(), 10, 64)
	if err != nil || upstreamID <= 0 {
		return nil, fmt.Errorf("invalid upstream task id")
	}

	chinaTime := time.FixedZone("CST", 8*60*60)
	start := time.Unix(task.SubmitTime, 0).In(chinaTime).Add(-time.Hour)
	end := time.Now().In(chinaTime).Add(time.Hour)
	for page := 0; page < 100; page++ {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		response, err := postJSON(a.baseURL, billListPath, a.apiKey, a.proxy, billListRequest{
			ExpenseDateStart: start.Format(time.DateTime),
			ExpenseDateEnd:   end.Format(time.DateTime),
			Page:             page,
			Size:             billPageSize,
		})
		if err != nil {
			return nil, err
		}
		body, readErr := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		if response.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("bill API returned HTTP %d", response.StatusCode)
		}
		var envelope upstreamEnvelope[*billListResponse]
		if err := common.Unmarshal(body, &envelope); err != nil {
			return nil, err
		}
		if envelope.State != 1 || envelope.Data == nil {
			return nil, fmt.Errorf("bill API rejected request: %v", envelope.Error)
		}
		for _, item := range envelope.Data.List {
			id, err := rawInt64(item.ID)
			if err != nil || id != upstreamID {
				continue
			}
			totalTokens, err := rawInt64(item.TotalTokens)
			if err != nil || totalTokens <= 0 {
				return nil, fmt.Errorf("bill record contains invalid total_tokens")
			}
			quota, clamp, err := quotaFromDomesticUsage(totalTokens, snapshot)
			if err != nil {
				return nil, err
			}
			return &service.TaskBillingResolution{
				ActualQuota:        quota,
				TotalTokens:        totalTokens,
				QuotaClamp:         clamp,
				SupplierPrice:      rawString(item.Price),
				SupplierDiscount:   rawString(item.Discount),
				SupplierAmountPaid: rawString(item.AmountPaid),
				ExpenseTime:        item.ExpenseTime,
			}, nil
		}
		total, err := rawInt64(envelope.Data.Total)
		if err != nil || int64((page+1)*billPageSize) >= total || len(envelope.Data.List) == 0 {
			break
		}
	}
	return nil, service.ErrTaskBillingRecordNotReady
}

func rawInt64(raw json.RawMessage) (int64, error) {
	if len(raw) == 0 {
		return 0, fmt.Errorf("integer value is empty")
	}
	var number json.Number
	if err := common.Unmarshal(raw, &number); err == nil {
		if value, parseErr := strconv.ParseInt(number.String(), 10, 64); parseErr == nil {
			return value, nil
		}
	}
	var text string
	if err := common.Unmarshal(raw, &text); err != nil {
		return 0, err
	}
	return strconv.ParseInt(strings.TrimSpace(text), 10, 64)
}

func rawString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if err := common.Unmarshal(raw, &text); err == nil {
		return text
	}
	return strings.TrimSpace(string(raw))
}
