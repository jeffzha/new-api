package seedancedomestic

import (
	"fmt"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relay/channel/task/taskcommon"
	"github.com/shopspring/decimal"
)

const (
	providerName = model.TaskBillingProviderSeedanceDomestic
	defaultModel = "doubao-seedance-2-0-260128"
	videoFPS     = int64(24)
)

var ModelList = []string{defaultModel}

var seedanceDomestic4KEnabled = common.GetEnvOrDefaultBool("SEEDANCE_DOMESTIC_4K_ENABLED", false)

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

func officialUnitPriceCNY(resolution string, hasVideo bool) decimal.Decimal {
	if strings.EqualFold(resolution, "4k") {
		if hasVideo {
			return decimal.NewFromInt(16)
		}
		return decimal.NewFromInt(26)
	}
	if strings.EqualFold(resolution, "1080p") {
		if hasVideo {
			return decimal.NewFromInt(31)
		}
		return decimal.NewFromInt(51)
	}
	if hasVideo {
		return decimal.NewFromInt(28)
	}
	return decimal.NewFromInt(46)
}

func estimateVideoTokens(request *generateRequest, videoCount int) int64 {
	duration := int64(request.Dur)
	if duration == -1 {
		duration = 15
	}
	if videoCount > 0 {
		duration += int64(videoCount) * 15
	}
	pixels := outputPixels(request.Resolution, request.Ratio)
	return decimal.NewFromInt(duration).
		Mul(decimal.NewFromInt(pixels)).
		Mul(decimal.NewFromInt(videoFPS)).
		Div(decimal.NewFromInt(1024)).
		Ceil().
		IntPart()
}

func outputPixels(resolution string, ratio string) int64 {
	byRatio := resolutionPixels[strings.ToLower(resolution)]
	if pixels := byRatio[ratio]; pixels > 0 {
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

func quotaFromDomesticUsage(totalTokens int64, snapshot *model.TaskProviderBillingSnapshot) (int, *common.QuotaClamp, error) {
	if snapshot == nil || snapshot.Provider != providerName {
		return 0, nil, fmt.Errorf("missing Seedance domestic billing snapshot")
	}
	return taskcommon.QuotaFromCNYPerMillionTokens(totalTokens, snapshot)
}
