package hwdramaproxy

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/tidwall/gjson"
)

type scopedAssetRequest struct {
	Operation   string
	NamespaceID string
	ScopeID     string
	ChannelID   int
	TokenID     int
	ResourceID  string
	GroupID     string
}

type scopedAssetError struct {
	Status  int
	Code    string
	Message string
}

func (err *scopedAssetError) Error() string {
	return err.Message
}

func prepareScopedAssetRequest(
	operation string,
	pathResourceID string,
	body []byte,
	route RouteMatch,
	tokenID int,
) ([]byte, *scopedAssetRequest, bool, error) {
	if route.AssetScopeID == "" {
		return body, nil, false, nil
	}
	if operation == "" {
		return nil, nil, false, &scopedAssetError{
			Status:  http.StatusInternalServerError,
			Code:    "configuration_error",
			Message: "scoped asset route has no scope operation",
		}
	}
	if err := model.UpsertAssetScopeTokenBinding(
		route.AssetNamespaceID,
		route.AssetScopeID,
		route.ChannelID,
		tokenID,
	); err != nil {
		return nil, nil, false, &scopedAssetError{
			Status:  http.StatusInternalServerError,
			Code:    "database_error",
			Message: "failed to register API token asset scope",
		}
	}

	request := &scopedAssetRequest{
		Operation:   operation,
		NamespaceID: route.AssetNamespaceID,
		ScopeID:     route.AssetScopeID,
		ChannelID:   route.ChannelID,
		TokenID:     tokenID,
		ResourceID:  strings.TrimSpace(pathResourceID),
	}
	switch operation {
	case ScopeOperationAssetCreate:
		groupID, err := requiredStringField(body, "groupId")
		if err != nil {
			return nil, nil, false, err
		}
		allowed, dbErr := model.AssetGroupBelongsToScope(groupID, request.NamespaceID, request.ScopeID, request.ChannelID)
		if dbErr != nil {
			return nil, nil, false, databaseScopeError("failed to verify asset group scope")
		}
		if !allowed {
			return nil, nil, false, forbiddenScopeError("asset group does not belong to this API key scope")
		}
		request.GroupID = groupID
	case ScopeOperationAssetList, ScopeOperationAssetGroupList:
		ownedGroupIDs, err := model.ListAssetGroupIDsForScope(request.NamespaceID, request.ScopeID, request.ChannelID)
		if err != nil {
			return nil, nil, false, databaseScopeError("failed to load asset group scope")
		}
		if len(ownedGroupIDs) == 0 {
			return body, request, true, nil
		}
		restrictedBody, err := restrictListToOwnedGroups(body, ownedGroupIDs)
		if err != nil {
			return nil, nil, false, err
		}
		body = restrictedBody
	case ScopeOperationAssetGet, ScopeOperationAssetUpdate, ScopeOperationAssetDelete:
		if request.ResourceID == "" {
			return nil, nil, false, invalidScopeRequest("asset identifier is required")
		}
		allowed, err := model.AssetBelongsToScope(
			request.ResourceID,
			request.NamespaceID,
			request.ScopeID,
			request.ChannelID,
		)
		if err != nil {
			return nil, nil, false, databaseScopeError("failed to verify asset scope")
		}
		if !allowed {
			return nil, nil, false, forbiddenScopeError("asset does not belong to this API key scope")
		}
	case ScopeOperationAssetGroupGet, ScopeOperationAssetGroupUpdate, ScopeOperationAssetGroupDelete:
		if request.ResourceID == "" {
			return nil, nil, false, invalidScopeRequest("asset group identifier is required")
		}
		allowed, err := model.AssetGroupBelongsToScope(
			request.ResourceID,
			request.NamespaceID,
			request.ScopeID,
			request.ChannelID,
		)
		if err != nil {
			return nil, nil, false, databaseScopeError("failed to verify asset group scope")
		}
		if !allowed {
			return nil, nil, false, forbiddenScopeError("asset group does not belong to this API key scope")
		}
	case ScopeOperationRealPersonSessionCreate, ScopeOperationAssetGroupCreate:
	case ScopeOperationRealPersonGroupGet:
		bytedToken, err := requiredStringField(body, "bytedToken")
		if err != nil {
			return nil, nil, false, err
		}
		allowed, dbErr := model.AssetAuthSessionBelongsToScope(
			bytedToken,
			request.NamespaceID,
			request.ScopeID,
			request.ChannelID,
		)
		if dbErr != nil {
			return nil, nil, false, databaseScopeError("failed to verify real-person authentication session")
		}
		if !allowed {
			return nil, nil, false, forbiddenScopeError("real-person authentication session does not belong to this API key scope")
		}
	default:
		return nil, nil, false, &scopedAssetError{
			Status:  http.StatusInternalServerError,
			Code:    "configuration_error",
			Message: "unsupported asset scope operation",
		}
	}
	return body, request, false, nil
}

func persistScopedAssetResponse(body []byte, request *scopedAssetRequest) error {
	if request == nil {
		return nil
	}
	switch request.Operation {
	case ScopeOperationAssetCreate:
		assetID := responseString(body, "body")
		if assetID == "" {
			assetID = responseString(body, "body.assetId")
		}
		if assetID != "" {
			return model.UpsertScopedAssetChannelBinding(
				assetID,
				request.GroupID,
				request.NamespaceID,
				request.ScopeID,
				request.ChannelID,
				request.TokenID,
			)
		}
	case ScopeOperationAssetList:
		for _, item := range gjson.GetBytes(body, "body.data").Array() {
			assetID := strings.TrimSpace(item.Get("assetId").String())
			groupID := strings.TrimSpace(item.Get("groupId").String())
			if assetID == "" || groupID == "" {
				continue
			}
			allowed, err := model.AssetGroupBelongsToScope(
				groupID,
				request.NamespaceID,
				request.ScopeID,
				request.ChannelID,
			)
			if err != nil {
				return err
			}
			if !allowed {
				return upstreamScopeError("upstream returned an asset outside the requested scope")
			}
			if err := model.UpsertScopedAssetChannelBinding(
				assetID,
				groupID,
				request.NamespaceID,
				request.ScopeID,
				request.ChannelID,
				request.TokenID,
			); err != nil {
				return err
			}
		}
	case ScopeOperationAssetGet, ScopeOperationAssetUpdate:
		groupID := responseString(body, "body.groupId")
		if groupID == "" {
			return nil
		}
		allowed, err := model.AssetGroupBelongsToScope(
			groupID,
			request.NamespaceID,
			request.ScopeID,
			request.ChannelID,
		)
		if err != nil {
			return err
		}
		if !allowed {
			return upstreamScopeError("upstream returned an asset outside the requested scope")
		}
		return model.UpsertScopedAssetChannelBinding(
			request.ResourceID,
			groupID,
			request.NamespaceID,
			request.ScopeID,
			request.ChannelID,
			request.TokenID,
		)
	case ScopeOperationAssetDelete:
		return model.DeleteScopedAssetBinding(
			request.ResourceID,
			request.NamespaceID,
			request.ScopeID,
			request.ChannelID,
		)
	case ScopeOperationAssetGroupCreate:
		groupID := responseString(body, "body.groupId")
		if groupID != "" {
			return model.UpsertAssetGroupScopeBinding(
				groupID,
				request.NamespaceID,
				request.ScopeID,
				request.ChannelID,
				request.TokenID,
			)
		}
	case ScopeOperationAssetGroupList:
		for _, item := range gjson.GetBytes(body, "body.data").Array() {
			groupID := strings.TrimSpace(item.Get("groupId").String())
			if groupID == "" {
				continue
			}
			allowed, err := model.AssetGroupBelongsToScope(
				groupID,
				request.NamespaceID,
				request.ScopeID,
				request.ChannelID,
			)
			if err != nil {
				return err
			}
			if !allowed {
				return upstreamScopeError("upstream returned an asset group outside the requested scope")
			}
		}
	case ScopeOperationAssetGroupDelete:
		return model.DeleteScopedAssetGroupBinding(
			request.ResourceID,
			request.NamespaceID,
			request.ScopeID,
			request.ChannelID,
		)
	case ScopeOperationRealPersonSessionCreate:
		bytedToken := responseString(body, "body.bytedToken")
		if bytedToken == "" {
			return nil
		}
		return model.UpsertAssetAuthSessionBinding(
			bytedToken,
			request.NamespaceID,
			request.ScopeID,
			request.ChannelID,
			request.TokenID,
			gjson.GetBytes(body, "body.expiresIn").Int(),
		)
	case ScopeOperationRealPersonGroupGet:
		groupID := responseString(body, "body")
		if groupID != "" {
			return model.UpsertAssetGroupScopeBinding(
				groupID,
				request.NamespaceID,
				request.ScopeID,
				request.ChannelID,
				request.TokenID,
			)
		}
	}
	return nil
}

func restrictListToOwnedGroups(body []byte, ownedGroupIDs []string) ([]byte, error) {
	payload := make(map[string]json.RawMessage)
	if len(bytes.TrimSpace(body)) > 0 {
		if err := common.Unmarshal(body, &payload); err != nil {
			return nil, invalidScopeRequest("request body must be a JSON object")
		}
	}
	requestedGroupIDs := make([]string, 0)
	if raw, ok := payload["groupIds"]; ok && len(bytes.TrimSpace(raw)) > 0 && string(bytes.TrimSpace(raw)) != "null" {
		if err := common.Unmarshal(raw, &requestedGroupIDs); err != nil {
			return nil, invalidScopeRequest("groupIds must be an array of strings")
		}
	}
	owned := make(map[string]bool, len(ownedGroupIDs))
	for _, groupID := range ownedGroupIDs {
		owned[groupID] = true
	}
	if len(requestedGroupIDs) == 0 {
		requestedGroupIDs = ownedGroupIDs
	} else {
		for _, groupID := range requestedGroupIDs {
			if !owned[strings.TrimSpace(groupID)] {
				return nil, forbiddenScopeError("requested asset group does not belong to this API key scope")
			}
		}
	}
	rawGroupIDs, err := common.Marshal(requestedGroupIDs)
	if err != nil {
		return nil, databaseScopeError("failed to encode scoped asset query")
	}
	payload["groupIds"] = rawGroupIDs
	restrictedBody, err := common.Marshal(payload)
	if err != nil {
		return nil, invalidScopeRequest("failed to encode request body")
	}
	return restrictedBody, nil
}

func requiredStringField(body []byte, field string) (string, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return "", invalidScopeRequest(field + " is required")
	}
	result := gjson.GetBytes(body, field)
	if result.Type != gjson.String || strings.TrimSpace(result.String()) == "" {
		return "", invalidScopeRequest(field + " is required")
	}
	return strings.TrimSpace(result.String()), nil
}

func responseString(body []byte, path string) string {
	result := gjson.GetBytes(body, path)
	if result.Type != gjson.String {
		return ""
	}
	return strings.TrimSpace(result.String())
}

func emptyScopedListResponse() []byte {
	body, err := common.Marshal(map[string]interface{}{
		"state":        "OK",
		"errorCode":    "",
		"errorMessage": "",
		"body": map[string]interface{}{
			"total": 0,
			"data":  []interface{}{},
		},
	})
	if err != nil {
		return []byte(`{"state":"OK","body":{"total":0,"data":[]}}`)
	}
	return body
}

func invalidScopeRequest(message string) *scopedAssetError {
	return &scopedAssetError{
		Status:  http.StatusBadRequest,
		Code:    "invalid_request",
		Message: message,
	}
}

func forbiddenScopeError(message string) *scopedAssetError {
	return &scopedAssetError{
		Status:  http.StatusForbidden,
		Code:    "asset_scope_forbidden",
		Message: message,
	}
}

func databaseScopeError(message string) *scopedAssetError {
	return &scopedAssetError{
		Status:  http.StatusInternalServerError,
		Code:    "database_error",
		Message: message,
	}
}

func upstreamScopeError(message string) *scopedAssetError {
	return &scopedAssetError{
		Status:  http.StatusBadGateway,
		Code:    "upstream_error",
		Message: message,
	}
}
