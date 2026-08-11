package workbenchbridge

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
)

const (
	controlTicketIssuePath       = "/api/internal/workbench/entry-tickets/issue"
	controlServiceHeader         = "X-Workbench-Service"
	controlTimestampHeader       = "X-Workbench-Timestamp"
	controlNonceHeader           = "X-Workbench-Nonce"
	controlSignatureHeader       = "X-Workbench-Signature"
	controlContractVersionHeader = "X-Workbench-Contract-Version"
	controlContractVersion       = "1"
	controlResponseTimeHeader    = "X-Workbench-Response-Timestamp"
	controlResponseNonceHeader   = "X-Workbench-Response-Nonce"
	controlResponseSignatureHead = "X-Workbench-Response-Signature"
	maximumControlResponseBytes  = 64 << 10
)

var ErrControlUnavailable = errors.New("workbench control service is unavailable")

const (
	SurfaceWorkbench = "workbench"
	SurfaceAdmin     = "admin"
)

type IssuedTicket struct {
	Value     string
	ExpiresAt time.Time
}

type TicketIssuer interface {
	Issue(ctx context.Context, request TicketIssueRequest) (IssuedTicket, error)
}

type TicketIssueRequest struct {
	UserID          int
	IdentityVersion string
	Surface         string
	IsSuperAdmin    bool
	AuthenticatedAt time.Time
	AMR             []string
	ReauthNonce     string
}

type HTTPDoer interface {
	Do(request *http.Request) (*http.Response, error)
}

type ControlClient struct {
	baseURL     string
	serviceName string
	secret      []byte
	httpClient  HTTPDoer
	now         func() time.Time
	random      io.Reader
}

type controlEnvelope struct {
	Success bool `json:"success"`
	Data    struct {
		Ticket    string `json:"ticket"`
		ExpiresAt int64  `json:"expires_at"`
	} `json:"data"`
}

func NewControlClient(config Config) (*ControlClient, error) {
	if !config.Enabled {
		return nil, ErrInvalidConfiguration
	}
	if err := config.ValidateTicketIssuer(); err != nil {
		return nil, err
	}
	return &ControlClient{
		baseURL:     config.ControlURL,
		serviceName: config.ControlServiceName,
		secret:      append([]byte(nil), config.ControlHMACSecret...),
		httpClient: &http.Client{
			Timeout: config.ControlTimeout,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
		now:    time.Now,
		random: rand.Reader,
	}, nil
}

func (client *ControlClient) Issue(ctx context.Context, ticketRequest TicketIssueRequest) (IssuedTicket, error) {
	ticketRequest.IdentityVersion = strings.TrimSpace(ticketRequest.IdentityVersion)
	ticketRequest.Surface = strings.TrimSpace(ticketRequest.Surface)
	if ticketRequest.UserID <= 0 || ticketRequest.IdentityVersion == "" || (ticketRequest.Surface != SurfaceWorkbench && ticketRequest.Surface != SurfaceAdmin) {
		return IssuedTicket{}, ErrInvalidConfiguration
	}
	if ticketRequest.Surface == SurfaceAdmin && !ticketRequest.IsSuperAdmin {
		return IssuedTicket{}, ErrInvalidConfiguration
	}
	payload := map[string]any{
		"new_api_user_id":  ticketRequest.UserID,
		"identity_version": ticketRequest.IdentityVersion,
		"surface":          ticketRequest.Surface,
		"is_super_admin":   ticketRequest.IsSuperAdmin,
	}
	if !ticketRequest.AuthenticatedAt.IsZero() || len(ticketRequest.AMR) > 0 || strings.TrimSpace(ticketRequest.ReauthNonce) != "" {
		if ticketRequest.Surface != SurfaceAdmin || ticketRequest.AuthenticatedAt.IsZero() || len(ticketRequest.AMR) == 0 || strings.TrimSpace(ticketRequest.ReauthNonce) == "" {
			return IssuedTicket{}, ErrInvalidConfiguration
		}
		payload["authenticated_at"] = ticketRequest.AuthenticatedAt.UTC().Unix()
		payload["amr"] = ticketRequest.AMR
		payload["reauth_nonce"] = strings.TrimSpace(ticketRequest.ReauthNonce)
	}
	body, err := common.Marshal(payload)
	if err != nil {
		return IssuedTicket{}, err
	}

	nonceBytes := make([]byte, 24)
	if _, err = io.ReadFull(client.random, nonceBytes); err != nil {
		return IssuedTicket{}, fmt.Errorf("%w: generate request nonce", ErrControlUnavailable)
	}
	nonce := base64.RawURLEncoding.EncodeToString(nonceBytes)
	timestamp := client.now().UTC().Unix()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.baseURL+controlTicketIssuePath, bytes.NewReader(body))
	if err != nil {
		return IssuedTicket{}, fmt.Errorf("%w: create request", ErrControlUnavailable)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(controlContractVersionHeader, controlContractVersion)
	request.Header.Set(controlServiceHeader, client.serviceName)
	request.Header.Set(controlTimestampHeader, strconv.FormatInt(timestamp, 10))
	request.Header.Set(controlNonceHeader, nonce)
	request.Header.Set(controlSignatureHeader, signControlRequest(client.secret, request.Method, controlTicketIssuePath, timestamp, nonce, body))

	response, err := client.httpClient.Do(request)
	if err != nil {
		return IssuedTicket{}, fmt.Errorf("%w: request failed", ErrControlUnavailable)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maximumControlResponseBytes+1))
	if err != nil || len(responseBody) > maximumControlResponseBytes {
		return IssuedTicket{}, fmt.Errorf("%w: invalid response body", ErrControlUnavailable)
	}
	if err = verifyControlResponse(
		client.secret,
		response.StatusCode,
		controlTicketIssuePath,
		response.Header.Get(controlContractVersionHeader),
		response.Header.Get(controlResponseTimeHeader),
		response.Header.Get(controlResponseNonceHeader),
		response.Header.Get(controlResponseSignatureHead),
		nonce,
		responseBody,
		client.now(),
	); err != nil {
		return IssuedTicket{}, err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return IssuedTicket{}, fmt.Errorf("%w: control returned status %d", ErrControlUnavailable, response.StatusCode)
	}

	var envelope controlEnvelope
	if err = common.Unmarshal(responseBody, &envelope); err != nil {
		return IssuedTicket{}, fmt.Errorf("%w: invalid ticket response", ErrControlUnavailable)
	}
	expiresAt := time.Unix(envelope.Data.ExpiresAt, 0).UTC()
	if !envelope.Success || envelope.Data.Ticket == "" || !expiresAt.After(client.now()) {
		return IssuedTicket{}, fmt.Errorf("%w: invalid ticket response", ErrControlUnavailable)
	}
	return IssuedTicket{Value: envelope.Data.Ticket, ExpiresAt: expiresAt}, nil
}

func signControlRequest(secret []byte, method string, path string, timestamp int64, nonce string, body []byte) string {
	bodyDigest := sha256.Sum256(body)
	canonical := fmt.Sprintf("%s\n%s\n%s\n%d\n%s\n%s", controlContractVersion, strings.ToUpper(method), path, timestamp, nonce, hex.EncodeToString(bodyDigest[:]))
	signer := hmac.New(sha256.New, secret)
	_, _ = signer.Write([]byte(canonical))
	return hex.EncodeToString(signer.Sum(nil))
}

func verifyControlResponse(
	secret []byte,
	status int,
	path string,
	contractVersionHeader string,
	timestampHeader string,
	nonceHeader string,
	signatureHeader string,
	requestNonce string,
	body []byte,
	now time.Time,
) error {
	if strings.TrimSpace(contractVersionHeader) != controlContractVersion {
		return ErrControlUnavailable
	}
	timestamp, err := strconv.ParseInt(strings.TrimSpace(timestampHeader), 10, 64)
	if err != nil || !hmac.Equal([]byte(strings.TrimSpace(nonceHeader)), []byte(requestNonce)) {
		return ErrControlUnavailable
	}
	delta := now.Sub(time.Unix(timestamp, 0))
	if delta < -time.Minute || delta > time.Minute {
		return ErrControlUnavailable
	}
	bodyDigest := sha256.Sum256(body)
	canonical := fmt.Sprintf("%s\n%d\n%s\n%d\n%s\n%s", controlContractVersion, status, path, timestamp, requestNonce, hex.EncodeToString(bodyDigest[:]))
	signer := hmac.New(sha256.New, secret)
	_, _ = signer.Write([]byte(canonical))
	provided, err := hex.DecodeString(strings.TrimSpace(signatureHeader))
	if err != nil || !hmac.Equal(signer.Sum(nil), provided) {
		return ErrControlUnavailable
	}
	return nil
}
