package opsbridge

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
)

// RelayType is deliberately closed. Unsupported billable routes fail before
// contacting an upstream when call enforcement is enabled.
func RelayType(method, path string) string {
	if method != "POST" {
		return ""
	}
	switch {
	case strings.HasSuffix(path, "/chat/completions"), strings.HasSuffix(path, "/completions"),
		strings.HasSuffix(path, "/messages"), strings.HasSuffix(path, "/responses"),
		strings.HasSuffix(path, ":generateContent"), strings.HasSuffix(path, ":streamGenerateContent"):
		return "text_generation"
	case strings.HasSuffix(path, "/embeddings"):
		return "embedding"
	case strings.HasSuffix(path, "/images/generations"), strings.HasSuffix(path, "/images/edits"), path == "/v1/edits":
		return "image_generation"
	case strings.HasSuffix(path, "/audio/transcriptions"), strings.HasSuffix(path, "/audio/translations"):
		return "audio_transcription"
	case strings.HasSuffix(path, "/audio/speech"):
		return "text_to_speech"
	case strings.HasSuffix(path, "/rerank"):
		return "rerank"
	case strings.HasSuffix(path, "/moderations"):
		return "moderation"
	default:
		return ""
	}
}

func CallGate() gin.HandlerFunc {
	return func(c *gin.Context) {
		b := Default
		if b == nil || !b.cfg.EnforceCalls {
			c.Next()
			return
		}
		kind := RelayType(c.Request.Method, c.Request.URL.Path)
		requestID := c.GetString(common.RequestIdKey)
		model := c.GetString("original_model")
		if kind == "" || model == "" || requestID == "" || c.GetInt("id") <= 0 {
			c.AbortWithStatusJSON(409, gin.H{"error": gin.H{"code": "ops_unsupported_call", "message": "This route is unavailable with Ops call enforcement"}})
			return
		}
		if err := b.setRequest(requestID, "reserving"); err != nil {
			c.AbortWithStatusJSON(503, gin.H{"error": gin.H{"code": "ops_state_unavailable"}})
			return
		}
		body := map[string]any{"request_id": requestID, "downstream_customer_key": strconv.Itoa(c.GetInt("id")),
			"routing_context": map[string]any{"model_name": model, "call_type": kind}}
		if token := c.GetInt("token_id"); token > 0 {
			body["api_key_id"] = strconv.Itoa(token)
		}
		var decision struct {
			Decision string `json:"decision"`
		}
		err := b.request(c.Request.Context(), "POST", "call-reservations", requestID, body, &decision)
		if err != nil || decision.Decision != "allowed" {
			status := http.StatusTooManyRequests
			if err != nil {
				status = http.StatusServiceUnavailable
			}
			// A lost response can still have created a reservation remotely.
			if err != nil {
				_ = b.Enqueue("call-reservations/cancel", requestID, map[string]any{"request_id": requestID})
			}
			_ = b.setRequest(requestID, "blocked")
			c.AbortWithStatusJSON(status, gin.H{"error": gin.H{"code": "ops_call_unavailable"}})
			return
		}
		if err := b.setRequest(requestID, "reserved"); err != nil {
			_ = b.Enqueue("call-reservations/cancel", requestID, map[string]any{"request_id": requestID})
			c.AbortWithStatusJSON(503, gin.H{"error": gin.H{"code": "ops_state_unavailable"}})
			return
		}
		ctx, cancel := context.WithCancel(c.Request.Context())
		c.Request = c.Request.WithContext(ctx)
		done := make(chan struct{})
		go func() {
			defer close(done)
			ticker := time.NewTicker(time.Minute)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if err := b.request(ctx, "POST", "call-reservations/renew", requestID, map[string]any{"request_id": requestID}, nil); err != nil {
						_ = b.setRequest(requestID, "lease_lost")
						cancel()
						return
					}
				}
			}
		}()
		defer func() {
			cancel()
			<-done
			if err := b.finishRequest(requestID, c.Writer.Status() >= 400); err != nil {
				common.SysError(err.Error())
			}
		}()
		c.Next()
	}
}

func (b *Bridge) finishRequest(id string, failed bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state.Requests[id] == "usage_queued" {
		return nil
	}
	if failed {
		return b.enqueueLocked("call-reservations/cancel", id, map[string]any{"request_id": id}, id, "cancel_queued")
	}
	b.state.Requests[id] = "needs_usage_evidence"
	return b.saveLocked()
}

func (b *Bridge) MarkUsageGap(id string) error {
	if id == "" {
		return errors.New("missing request identity")
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.state.Requests[id] == "usage_queued" {
		return nil
	}
	if b.state.Requests[id] == "" && len(b.state.Requests) >= 10000 {
		return errors.New("Ops evidence capacity exceeded")
	}
	b.state.Requests[id] = "needs_usage_evidence"
	return b.saveLocked()
}

func (b *Bridge) setRequest(id, status string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, exists := b.state.Requests[id]; !exists && (len(b.state.Requests) >= 10000 || len(b.state.Deliveries) >= 9900) {
		return errors.New("Ops evidence capacity exceeded")
	}
	if (status == "reserving" || status == "task_reserving") && b.state.Requests[id] != "" {
		return errors.New("duplicate Ops request identity")
	}
	if status == "reserving" || status == "task_reserving" {
		_, err := os.Stat(filepath.Join(b.cfg.StateDir, "archive", "request-"+hash([]byte(id))+".json"))
		if err == nil {
			return errors.New("duplicate Ops request identity")
		}
		if !os.IsNotExist(err) {
			return errors.New("cannot read archived request evidence")
		}
	}
	b.state.Requests[id] = status
	return b.saveLocked()
}

func (b *Bridge) RecordUsage(requestID string, record map[string]any) error {
	batchID := "usage-" + requestID
	body := map[string]any{"batchId": batchID, "adapter": "new-api", "adapterVersion": "1", "processing": "async", "records": []any{record}}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.enqueueLocked("native-usage/bulk", batchID, body, requestID, "usage_queued")
}
