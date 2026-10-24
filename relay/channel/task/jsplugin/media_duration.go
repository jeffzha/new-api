package jsplugin

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/service"
	"github.com/abema/go-mp4"
	"github.com/gin-gonic/gin"
)

type mediaProbe struct {
	Key    string `json:"key"`
	Source any    `json:"source"`
}

const maxMediaProbeBytes = 16 << 20

var mediaProbeSlots = make(chan struct{}, 4)
var mediaProbeKey = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)

// Probing happens outside JavaScript, before pre-consumption and submission.
// There is no provider key on these user-controlled, SSRF-protected downloads.
func probeMediaDurations(c *gin.Context, probes []mediaProbe) (map[string]float64, error) {
	if len(probes) > 4 {
		return nil, fmt.Errorf("at most four media duration probes are supported")
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 8*time.Second)
	defer cancel()
	select {
	case mediaProbeSlots <- struct{}{}:
		defer func() { <-mediaProbeSlots }()
	case <-ctx.Done():
		return nil, fmt.Errorf("reference video verification is busy; please try again later")
	}
	result := make(map[string]float64, len(probes))
	remaining := int64(maxMediaProbeBytes)
	for _, probe := range probes {
		if !mediaProbeKey.MatchString(probe.Key) {
			return nil, fmt.Errorf("invalid media duration probe key")
		}
		if _, exists := result[probe.Key]; exists {
			return nil, fmt.Errorf("duplicate media duration probe key")
		}
		data, err := readMediaProbe(c, ctx, probe.Source, remaining)
		if err != nil {
			return nil, fmt.Errorf("reference video %s could not be verified: %s", probe.Key, err)
		}
		remaining -= int64(len(data))
		duration, err := mp4MediaDuration(ctx, data)
		if err != nil {
			return nil, fmt.Errorf("reference video %s has no valid MP4 duration; use a standard MP4 file", probe.Key)
		}
		result[probe.Key] = duration
	}
	return result, nil
}

func readMediaProbe(c *gin.Context, ctx context.Context, source any, limit int64) ([]byte, error) {
	var reader io.ReadCloser
	if placeholder, ok := source.(map[string]any); ok {
		ref, _ := placeholder["__fileRef"].(string)
		field, valid := strings.CutPrefix(ref, "request_file:")
		if !valid || field == "" {
			return nil, fmt.Errorf("invalid uploaded file reference")
		}
		form, err := common.ParseMultipartFormReusable(c)
		if err != nil {
			return nil, fmt.Errorf("cannot read uploaded file")
		}
		defer form.RemoveAll()
		files := form.File[field]
		if len(files) != 1 || files[0].Size > limit {
			return nil, fmt.Errorf("upload must contain one MP4 within the 16 MiB probe limit")
		}
		reader, err = files[0].Open()
		if err != nil {
			return nil, fmt.Errorf("cannot open uploaded file")
		}
	} else if raw, ok := source.(string); ok {
		if strings.HasPrefix(raw, "data:") {
			header, payload, found := strings.Cut(raw, ",")
			if !found || header != "data:video/mp4;base64" || int64(base64.StdEncoding.DecodedLen(len(payload))) > limit+2 {
				return nil, fmt.Errorf("use a video/mp4 Base64 data URL within the 16 MiB probe limit")
			}
			data, err := base64.StdEncoding.DecodeString(payload)
			if err != nil || int64(len(data)) > limit {
				return nil, fmt.Errorf("invalid or oversized MP4 data URL")
			}
			return data, nil
		}
		parsed, err := url.Parse(raw)
		if err != nil || parsed.User != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return nil, fmt.Errorf("use a public HTTP(S) MP4 URL")
		}
		if err := service.ValidateSSRFProtectedFetchURL(raw); err != nil {
			return nil, fmt.Errorf("video URL is not allowed by the fetch policy")
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
		if err != nil {
			return nil, fmt.Errorf("invalid video URL")
		}
		client := service.GetSSRFProtectedHTTPClient()
		if client == nil {
			return nil, fmt.Errorf("media download service unavailable")
		}
		response, err := client.Do(request)
		if err != nil {
			return nil, fmt.Errorf("video download failed or exceeded 8 seconds")
		}
		reader = response.Body
		if response.StatusCode != http.StatusOK || response.ContentLength > limit {
			reader.Close()
			return nil, fmt.Errorf("video download returned HTTP %d or exceeded the 16 MiB probe limit", response.StatusCode)
		}
	} else {
		return nil, fmt.Errorf("invalid media source")
	}
	defer reader.Close()
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil || int64(len(data)) > limit || ctx.Err() != nil {
		return nil, fmt.Errorf("video download failed or exceeded the probe size/time limit")
	}
	return data, nil
}

func mp4MediaDuration(ctx context.Context, data []byte) (float64, error) {
	reader := bytes.NewReader(data)
	var duration float64
	boxes := 0
	_, err := mp4.ReadBoxStructure(reader, func(handle *mp4.ReadHandle) (any, error) {
		boxes++
		if ctx.Err() != nil || boxes > 256 {
			return nil, fmt.Errorf("MP4 probe limit exceeded")
		}
		if handle.BoxInfo.Offset > uint64(len(data)) || handle.BoxInfo.Size < handle.BoxInfo.HeaderSize || handle.BoxInfo.Size > uint64(len(data))-handle.BoxInfo.Offset {
			return nil, fmt.Errorf("invalid MP4 box size")
		}
		if len(handle.Path) == 1 && handle.BoxInfo.Type == mp4.BoxTypeMoov() {
			return handle.Expand()
		}
		if len(handle.Path) == 2 && handle.BoxInfo.Type == mp4.BoxTypeMvhd() {
			if duration != 0 || handle.BoxInfo.Size > 256 {
				return nil, fmt.Errorf("invalid MP4 movie header")
			}
			payload, _, err := handle.ReadPayload()
			if err != nil {
				return nil, err
			}
			header, ok := payload.(*mp4.Mvhd)
			if !ok || header.Timescale == 0 {
				return nil, fmt.Errorf("missing MP4 timescale")
			}
			duration = float64(header.GetDuration()) / float64(header.Timescale)
		}
		return nil, nil
	})
	if err != nil || duration <= 0 || math.IsNaN(duration) || math.IsInf(duration, 0) || duration > 3600 {
		return 0, fmt.Errorf("invalid MP4 duration")
	}
	return duration, nil
}
