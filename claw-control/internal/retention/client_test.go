package retention

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/claw-control/internal/jsonx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHTTPDeliveryClientSignsIntentAndRejectsUnsignedReceiptChanges(t *testing.T) {
	const secret = "retention-test-secret-0123456789abcdef"
	alterSignature := false
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		require.NoError(t, err)
		assert.Equal(t, "/api/internal/workbench/retention/intents", r.URL.Path)
		assert.Equal(t, "claw-control", r.Header.Get("X-Workbench-Service"))
		bodyHash := sha256.Sum256(body)
		canonical := strings.Join([]string{"2", http.MethodPost, r.URL.Path, r.Header.Get("X-Workbench-Timestamp"), r.Header.Get("X-Workbench-Nonce"), hex.EncodeToString(bodyHash[:])}, "\n")
		mac := hmac.New(sha256.New, []byte(secret))
		_, _ = mac.Write([]byte(canonical))
		assert.Equal(t, hex.EncodeToString(mac.Sum(nil)), r.Header.Get("X-Workbench-Signature"))
		var intent DeliveryIntent
		require.NoError(t, jsonx.Unmarshal(body, &intent))
		assert.False(t, intent.LegalHold)

		responseBody, err := jsonx.Marshal(map[string]any{"success": true, "data": map[string]any{
			"receipt_id": "receipt-1", "intent_id": intent.IntentID, "status": "completed", "counts": map[string]int{"conversations": 1},
		}})
		require.NoError(t, err)
		timestamp := strconv.FormatInt(time.Now().UTC().Unix(), 10)
		responseHash := sha256.Sum256(responseBody)
		responseCanonical := strings.Join([]string{"2", "200", r.URL.Path, timestamp, r.Header.Get("X-Workbench-Nonce"), hex.EncodeToString(responseHash[:])}, "\n")
		responseMAC := hmac.New(sha256.New, []byte(secret))
		_, _ = responseMAC.Write([]byte(responseCanonical))
		signature := hex.EncodeToString(responseMAC.Sum(nil))
		if alterSignature {
			signature = strings.Repeat("0", 64)
		}
		w.Header().Set("X-Workbench-Contract-Version", "2")
		w.Header().Set("X-Workbench-Response-Timestamp", timestamp)
		w.Header().Set("X-Workbench-Response-Nonce", r.Header.Get("X-Workbench-Nonce"))
		w.Header().Set("X-Workbench-Response-Signature", signature)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(responseBody)
	}))
	defer server.Close()

	client, err := NewHTTPDeliveryClient(server.URL+"/api/internal/workbench/retention/intents", secret, 5*time.Second, time.Minute, true)
	require.NoError(t, err)
	// httptest uses a private CA; retain the production redirect/timeout behavior
	// while replacing only the transport in this package-level contract test.
	serverClient := server.Client()
	serverClient.Timeout = 5 * time.Second
	// Constructing against HTTPS is validated above; use the test server's trusted transport.
	client.client = serverClient

	receipt, err := client.Deliver(context.Background(), DeliveryIntent{IntentID: "intent-1", CustomerID: 7, PolicyVersion: 3, CutoffAt: time.Now().UTC(), LegalHold: false})
	require.NoError(t, err)
	assert.Equal(t, "receipt-1", receipt.ReceiptID)

	alterSignature = true
	_, err = client.Deliver(context.Background(), DeliveryIntent{IntentID: "intent-2", CustomerID: 7, PolicyVersion: 3, CutoffAt: time.Now().UTC(), LegalHold: false})
	assert.ErrorContains(t, err, "signature")
}
