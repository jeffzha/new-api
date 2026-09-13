package openaiseedance

import (
	"strings"

	seedancepricing "github.com/QuantumNous/new-api/setting/seedance_video_pricing"
)

const (
	providerName = "openai_seedance"
	fps          = int64(24)
)

var resolutionPixels = map[string]map[string]int64{
	"720p": {
		"16:9": 1280 * 720,
		"4:3":  1112 * 834,
		"1:1":  960 * 960,
		"3:4":  834 * 1112,
		"9:16": 720 * 1280,
		"21:9": 1470 * 630,
	},
	"1080p": {
		"16:9": 1920 * 1080,
		"4:3":  1664 * 1248,
		"1:1":  1440 * 1440,
		"3:4":  1248 * 1664,
		"9:16": 1080 * 1920,
		"21:9": 2206 * 946,
	},
	"4k": {
		"16:9": 3840 * 2160,
		"4:3":  3326 * 2494,
		"1:1":  2880 * 2880,
		"3:4":  2494 * 3326,
		"9:16": 2160 * 3840,
		"21:9": 4398 * 1886,
	},
}

// estimateVideoTokens approximates the number of video tokens a generated clip
// consumes so billing can pre-charge against the per-million-token CNY price.
// Mirrors the "Seedance Domestic" estimation: duration × output pixels × fps.
func estimateVideoTokens(duration int, resolution string, ratio string) int64 {
	if duration <= 0 {
		duration = 5
	}
	pixels := outputPixels(resolution, ratio)
	// decimal arithmetic guarded against overflow (billing safety).
	d := durationInt64(duration)
	if d > 3600 {
		d = 3600
	}
	tokens := d * pixels * fps / 1024
	if tokens < 0 {
		return 0
	}
	return tokens
}

func durationInt64(d int) int64 {
	i := int64(d)
	if i < 0 {
		return 0
	}
	return i
}

func outputPixels(resolution string, ratio string) int64 {
	normRes, _ := seedancepricing.NormalizeResolution(seedancepricing.StandardSeedanceModel, resolution)
	byRatio := resolutionPixels[strings.ToLower(normRes)]
	if byRatio == nil {
		return resolutionPixels["720p"]["16:9"]
	}
	if pixels := byRatio[strings.ToLower(ratio)]; pixels > 0 {
		return pixels
	}
	var maximum int64
	for _, pixels := range byRatio {
		if pixels > maximum {
			maximum = pixels
		}
	}
	return maximum
}
