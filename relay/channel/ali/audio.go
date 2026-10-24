package ali

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

type aliFunASRResponse struct {
	Output struct {
		Text *string `json:"text"`
	} `json:"output"`
	Usage struct {
		Duration *float64 `json:"duration"`
	} `json:"usage"`
	RequestID string `json:"request_id"`
	Code      string `json:"code"`
	Message   string `json:"message"`
}

const maxFunASRResponseBytes = 16 << 20

func aliFunASRHandler(c *gin.Context, resp *http.Response, info *relaycommon.RelayInfo) (any, *types.NewAPIError) {
	defer service.CloseResponseBodyGracefully(resp)
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxFunASRResponseBytes+1))
	if err != nil {
		return nil, types.NewOpenAIError(fmt.Errorf("read Fun-ASR-Flash response: %w", err), types.ErrorCodeReadResponseBodyFailed, http.StatusBadGateway)
	}
	if len(body) > maxFunASRResponseBytes {
		return nil, types.NewOpenAIError(errors.New("Fun-ASR-Flash response exceeds the 16 MiB limit"), types.ErrorCodeBadResponseBody, http.StatusBadGateway)
	}
	upstream, err := decodeFunASRResponse(body)
	if err != nil {
		return nil, types.NewOpenAIError(fmt.Errorf("decode Fun-ASR-Flash response: %w", err), types.ErrorCodeBadResponseBody, http.StatusBadGateway)
	}
	if upstream.RequestID != "" && c.GetString(common.UpstreamRequestIdKey) == "" {
		c.Set(common.UpstreamRequestIdKey, upstream.RequestID)
	}
	if upstream.Code != "" {
		return nil, types.WithOpenAIError(types.OpenAIError{
			Message: upstream.Message,
			Type:    upstream.Code,
			Param:   upstream.RequestID,
			Code:    upstream.Code,
		}, http.StatusBadGateway)
	}
	if upstream.Output.Text == nil {
		return nil, types.NewOpenAIError(errors.New("Fun-ASR-Flash response is missing output.text"), types.ErrorCodeBadResponseBody, http.StatusBadGateway)
	}
	if upstream.Usage.Duration == nil || math.IsNaN(*upstream.Usage.Duration) || math.IsInf(*upstream.Usage.Duration, 0) || *upstream.Usage.Duration <= 0 {
		return nil, types.NewOpenAIError(errors.New("Fun-ASR-Flash response is missing a valid usage.duration; request was not billed"), types.ErrorCodeBadResponseBody, http.StatusBadGateway)
	}
	if *upstream.Usage.Duration > helper.MaxFunASRFlashDurationSeconds {
		return nil, types.NewOpenAIError(fmt.Errorf("Fun-ASR-Flash usage.duration exceeds the %d-second limit", helper.MaxFunASRFlashDurationSeconds), types.ErrorCodeBadResponseBody, http.StatusBadGateway)
	}
	if info.BillingRequestInput != nil {
		info.BillingRequestInput.Usage = map[string]any{"seconds": *upstream.Usage.Duration}
	}

	transcription := dto.AudioResponse{Text: *upstream.Output.Text}
	request, _ := info.Request.(*dto.AudioRequest)
	responseFormat := "json"
	if request != nil && request.ResponseFormat != "" {
		responseFormat = strings.ToLower(strings.TrimSpace(request.ResponseFormat))
	}
	switch responseFormat {
	case "json":
		c.JSON(http.StatusOK, transcription)
	case "text":
		c.Data(http.StatusOK, "text/plain; charset=utf-8", []byte(transcription.Text))
	default:
		return nil, types.NewErrorWithStatusCode(fmt.Errorf("Fun-ASR-Flash supports response_format json or text; %q is unsupported", responseFormat), types.ErrorCodeInvalidRequest, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
	}

	return &dto.Usage{}, nil
}

func decodeFunASRResponse(body []byte) (aliFunASRResponse, error) {
	var response aliFunASRResponse
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return response, errors.New("empty response body")
	}
	if trimmed[0] == '{' {
		return response, common.Unmarshal(trimmed, &response)
	}

	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	var eventData strings.Builder
	mergeEvent := func() error {
		if eventData.Len() == 0 {
			return nil
		}
		data := strings.TrimSpace(eventData.String())
		eventData.Reset()
		if data == "[DONE]" {
			return nil
		}
		var event aliFunASRResponse
		if err := common.Unmarshal([]byte(data), &event); err != nil {
			return err
		}
		if event.Output.Text != nil {
			response.Output.Text = event.Output.Text
		}
		if event.Usage.Duration != nil {
			response.Usage.Duration = event.Usage.Duration
		}
		if event.RequestID != "" {
			response.RequestID = event.RequestID
		}
		if event.Code != "" {
			response.Code = event.Code
			response.Message = event.Message
		}
		return nil
	}
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if err := mergeEvent(); err != nil {
				return response, err
			}
			continue
		}
		if data, ok := strings.CutPrefix(line, "data:"); ok {
			if eventData.Len() > 0 {
				eventData.WriteByte('\n')
			}
			eventData.WriteString(strings.TrimSpace(data))
		}
	}
	if err := scanner.Err(); err != nil {
		return response, fmt.Errorf("read event stream: %w", err)
	}
	if err := mergeEvent(); err != nil {
		return response, err
	}
	if response.Output.Text == nil && response.Code == "" {
		return response, errors.New("event stream did not contain a transcription result")
	}
	return response, nil
}
