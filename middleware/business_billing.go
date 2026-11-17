package middleware

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/businessapi"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/gin-gonic/gin"
)

// BusinessBilling freezes conversion for the final receipt. Only clients that
// opt in buffer JSON or receive the additional terminal SSE chunk.
func BusinessBilling() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set(businessapi.ConversionKey, businessapi.Conversion{
			Currency:     operation_setting.GetQuotaDisplayType(),
			QuotaPerUnit: strconv.FormatFloat(common.QuotaPerUnit, 'f', -1, 64),
			ExchangeRate: strconv.FormatFloat(operation_setting.GetUsdToCurrencyRate(operation_setting.USDExchangeRate), 'f', -1, 64),
		})
		if c.Query("include") != "billing" {
			c.Next()
			return
		}
		original := c.Writer
		writer := &businessBillingWriter{ResponseWriter: original}
		c.Writer = writer
		defer func() { c.Writer = original }()
		c.Next()
		billing, ok := businessapi.BillingFromContext(c)
		if !ok {
			billing = businessapi.UnavailableBilling()
		}
		writer.finish(c.GetString(common.RequestIdKey), billing)
	}
}

type businessBillingWriter struct {
	gin.ResponseWriter
	body        bytes.Buffer
	passthrough bool
	stream      bool
}

func (w *businessBillingWriter) Write(data []byte) (int, error) {
	if strings.HasPrefix(w.Header().Get("Content-Type"), "text/event-stream") {
		w.stream = true
		if bytes.Equal(bytes.TrimSpace(data), []byte("data: [DONE]")) {
			return len(data), nil
		}
		return w.ResponseWriter.Write(data)
	}
	if w.passthrough {
		return w.ResponseWriter.Write(data)
	}
	// Bound extra memory. Large responses remain intact; the request ID header
	// still lets callers retrieve the same receipt through the detail endpoint.
	if w.body.Len()+len(data) > 8<<20 {
		w.passthrough = true
		if _, err := w.ResponseWriter.Write(w.body.Bytes()); err != nil {
			return 0, err
		}
		w.body.Reset()
		return w.ResponseWriter.Write(data)
	}
	return w.body.Write(data)
}

func (w *businessBillingWriter) WriteString(data string) (int, error) { return w.Write([]byte(data)) }
func (w *businessBillingWriter) WriteHeaderNow() {
	if w.stream || w.passthrough {
		w.ResponseWriter.WriteHeaderNow()
	}
}
func (w *businessBillingWriter) Flush() {
	if strings.HasPrefix(w.Header().Get("Content-Type"), "text/event-stream") {
		w.stream = true
	}
	if w.stream || w.passthrough {
		w.ResponseWriter.Flush()
	}
}
func (w *businessBillingWriter) Written() bool { return w.body.Len() > 0 || w.ResponseWriter.Written() }
func (w *businessBillingWriter) Size() int {
	if w.body.Len() > 0 {
		return w.body.Len()
	}
	return w.ResponseWriter.Size()
}

func (w *businessBillingWriter) finish(requestID string, billing businessapi.Billing) {
	if w.passthrough {
		return
	}
	if w.stream {
		if w.Status() == http.StatusOK {
			data, err := common.Marshal(gin.H{"object": "chat.completion.chunk", "choices": []any{}, "request_id": requestID, "billing": billing})
			if err == nil {
				_, _ = w.ResponseWriter.Write(append(append([]byte("data: "), data...), '\n', '\n'))
			}
		}
		_, _ = w.ResponseWriter.Write([]byte("data: [DONE]\n\n"))
		w.ResponseWriter.Flush()
		return
	}
	data := w.body.Bytes()
	if w.Status() == http.StatusOK {
		var fields map[string]json.RawMessage
		if common.Unmarshal(data, &fields) == nil && fields != nil {
			fields["request_id"], _ = common.Marshal(requestID)
			fields["billing"], _ = common.Marshal(billing)
			if enriched, err := common.Marshal(fields); err == nil {
				data = enriched
				w.Header().Del("Content-Length")
			}
		}
	}
	_, _ = w.ResponseWriter.Write(data)
}
