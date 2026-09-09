package common

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/relaykit/types"
	modelcompat "github.com/QuantumNous/new-api/setting/model_compatibility"
)

func ApplyModelCompatibility(jsonData []byte, info *RelayInfo) ([]byte, *types.NewAPIError) {
	if info == nil {
		return jsonData, nil
	}

	modelName := strings.TrimSpace(info.UpstreamModelName)
	if modelName == "" && info.ChannelMeta != nil {
		modelName = strings.TrimSpace(info.ChannelMeta.UpstreamModelName)
	}
	if modelName == "" {
		modelName = strings.TrimSpace(info.OriginModelName)
	}

	normalized, version, changes, err := modelcompat.Normalize(jsonData, modelcompat.RequestContext{
		ChannelType: info.GetChannelType(),
		ChannelID:   info.ChannelId,
		Group:       info.UsingGroup,
		Model:       modelName,
		RelayFormat: string(info.GetFinalRequestRelayFormat()),
	})
	if err != nil {
		if requestErr, ok := err.(*modelcompat.RequestError); ok {
			return nil, types.WithOpenAIError(types.OpenAIError{
				Message: requestErr.Message,
				Type:    "invalid_request_error",
				Code:    "model_parameter_unsupported",
				Param:   requestErr.Path,
			}, http.StatusBadRequest, types.ErrOptionWithSkipRetry())
		}
		return nil, types.NewError(
			fmt.Errorf("invalid model compatibility registry: %w", err),
			types.ErrorCodeChannelParamOverrideInvalid,
			types.ErrOptionWithSkipRetry(),
		)
	}
	if len(changes) == 0 {
		return normalized, nil
	}
	syncReasoningEffortAfterParamOverride(info, jsonData, normalized)

	for _, change := range changes {
		line := fmt.Sprintf("model_compatibility version=%s profile=%s action=%s path=%s", version, change.ProfileID, change.Action, change.Path)
		if change.Detail != "" {
			line += " detail=" + change.Detail
		}
		info.ParamOverrideAudit = append(info.ParamOverrideAudit, line)
	}
	return normalized, nil
}
