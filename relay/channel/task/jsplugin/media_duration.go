package jsplugin

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
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
	_ "golang.org/x/image/webp"
)

type mediaProbe struct {
	Key    string `json:"key"`
	Source any    `json:"source"`
}

type imageDimensions struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

const maxMediaProbeBytes = 16 << 20

var mediaProbeSlots = make(chan struct{}, 4)
var mediaProbeKey = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)

// Probing happens outside JavaScript, before pre-consumption and submission.
// There is no provider key on these user-controlled, SSRF-protected downloads.
func probeMedia(c *gin.Context, probes, images []mediaProbe) (map[string]float64, map[string]imageDimensions, error) {
	if len(probes)+len(images) > 4 {
		return nil, nil, fmt.Errorf("at most four media probes are supported")
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 8*time.Second)
	defer cancel()
	select {
	case mediaProbeSlots <- struct{}{}:
		defer func() { <-mediaProbeSlots }()
	case <-ctx.Done():
		return nil, nil, fmt.Errorf("reference media verification is busy; please try again later")
	}
	result := make(map[string]float64, len(probes))
	dimensions := make(map[string]imageDimensions, len(images))
	keys := make(map[string]bool, len(probes)+len(images))
	remaining := int64(maxMediaProbeBytes)
	for index, probe := range append(append([]mediaProbe{}, probes...), images...) {
		if !mediaProbeKey.MatchString(probe.Key) {
			return nil, nil, fmt.Errorf("invalid media probe key")
		}
		if keys[probe.Key] {
			return nil, nil, fmt.Errorf("duplicate media probe key")
		}
		keys[probe.Key] = true
		media := "video"
		if index >= len(probes) {
			media = "image"
		}
		data, err := readMediaProbe(c, ctx, probe.Source, remaining, media)
		if err != nil {
			return nil, nil, fmt.Errorf("reference %s %s could not be verified: %s", media, probe.Key, err)
		}
		remaining -= int64(len(data))
		if media == "image" {
			config, _, err := image.DecodeConfig(bytes.NewReader(data))
			if err != nil || config.Width <= 0 || config.Height <= 0 || config.Width > 32768 || config.Height > 32768 || ctx.Err() != nil {
				return nil, nil, fmt.Errorf("reference image %s dimensions could not be verified; use a valid PNG, JPEG, GIF or WebP, or specify ratio explicitly", probe.Key)
			}
			dimensions[probe.Key] = imageDimensions{Width: config.Width, Height: config.Height}
			continue
		}
		duration, err := mp4MediaDuration(ctx, data)
		if err != nil {
			return nil, nil, fmt.Errorf("reference video %s has no valid MP4 duration; use a standard MP4 file", probe.Key)
		}
		result[probe.Key] = duration
	}
	return result, dimensions, nil
}

func readMediaProbe(c *gin.Context, ctx context.Context, source any, limit int64, media string) ([]byte, error) {
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
			return nil, fmt.Errorf("upload must contain one %s within the 16 MiB probe limit", media)
		}
		reader, err = files[0].Open()
		if err != nil {
			return nil, fmt.Errorf("cannot open uploaded file")
		}
	} else if raw, ok := source.(string); ok {
		if strings.HasPrefix(raw, "data:") {
			header, payload, found := strings.Cut(raw, ",")
			validType := header == "data:video/mp4;base64"
			if media == "image" {
				validType = header == "data:image/png;base64" || header == "data:image/jpeg;base64" || header == "data:image/gif;base64" || header == "data:image/webp;base64"
			}
			if !found || !validType || int64(base64.StdEncoding.DecodedLen(len(payload))) > limit+2 {
				return nil, fmt.Errorf("use a supported %s Base64 data URL within the 16 MiB probe limit", media)
			}
			data, err := base64.StdEncoding.DecodeString(payload)
			if err != nil || int64(len(data)) > limit {
				return nil, fmt.Errorf("invalid or oversized %s data URL", media)
			}
			return data, nil
		}
		parsed, err := url.Parse(raw)
		if err != nil || parsed.User != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return nil, fmt.Errorf("use a public HTTP(S) %s URL", media)
		}
		if err := service.ValidateSSRFProtectedFetchURL(raw); err != nil {
			return nil, fmt.Errorf("%s URL is not allowed by the fetch policy", media)
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
		if err != nil {
			return nil, fmt.Errorf("invalid %s URL", media)
		}
		client := service.GetSSRFProtectedHTTPClient()
		if client == nil {
			return nil, fmt.Errorf("media download service unavailable")
		}
		response, err := client.Do(request)
		if err != nil {
			return nil, fmt.Errorf("%s download failed or exceeded 8 seconds", media)
		}
		reader = response.Body
		if response.StatusCode != http.StatusOK || response.ContentLength > limit {
			reader.Close()
			return nil, fmt.Errorf("%s download returned HTTP %d or exceeded the 16 MiB probe limit", media, response.StatusCode)
		}
	} else {
		return nil, fmt.Errorf("invalid media source")
	}
	defer reader.Close()
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil || int64(len(data)) > limit || ctx.Err() != nil {
		return nil, fmt.Errorf("%s download failed or exceeded the probe size/time limit", media)
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
