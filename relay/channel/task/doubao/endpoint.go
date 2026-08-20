package doubao

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/QuantumNous/new-api/relaykit/dto"
)

const (
	defaultSubmitPath = "/api/v3/contents/generations/tasks"
	defaultFetchPath  = "/api/v3/contents/generations/tasks/{task_id}"
)

func resolveEndpointPaths(settings dto.ChannelOtherSettings) (string, string) {
	submitPath := defaultSubmitPath
	fetchPath := defaultFetchPath
	if settings.DoubaoVideoEndpoints == nil {
		return submitPath, fetchPath
	}
	if configured := strings.TrimSpace(settings.DoubaoVideoEndpoints.SubmitPath); configured != "" {
		submitPath = configured
	}
	if configured := strings.TrimSpace(settings.DoubaoVideoEndpoints.FetchPath); configured != "" {
		fetchPath = configured
	}
	return submitPath, fetchPath
}

func buildUpstreamURL(baseURL string, endpointPath string) (string, error) {
	baseURL = strings.TrimSpace(baseURL)
	endpointPath = strings.TrimSpace(endpointPath)
	if baseURL == "" {
		return "", fmt.Errorf("upstream base URL is empty")
	}
	if !strings.HasPrefix(endpointPath, "/") {
		return "", fmt.Errorf("upstream endpoint path must begin with /")
	}
	base, err := url.Parse(baseURL)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return "", fmt.Errorf("invalid upstream base URL")
	}
	if base.Scheme != "http" && base.Scheme != "https" {
		return "", fmt.Errorf("unsupported upstream base URL scheme %q", base.Scheme)
	}
	endpoint, err := url.Parse(endpointPath)
	if err != nil {
		return "", fmt.Errorf("invalid upstream endpoint path: %w", err)
	}
	if endpoint.IsAbs() || endpoint.Host != "" || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return "", fmt.Errorf("upstream endpoint must be a relative path without host, query, or fragment")
	}
	return strings.TrimRight(baseURL, "/") + endpointPath, nil
}
