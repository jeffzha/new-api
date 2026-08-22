package doubao

import "strings"

const seedance25Model = "dreamina-seedance-2-5-filter-off"

var ModelList = []string{
	"doubao-seedance-1-0-pro-250528",
	"doubao-seedance-1-0-lite-t2v",
	"doubao-seedance-1-0-lite-i2v",
	"doubao-seedance-1-5-pro-251215",
	"doubao-seedance-2-0-260128",
	"doubao-seedance-2-0-fast-260128",
	"doubao-seedance-2-0-filter-off",
	"doubao-seedance-2-0-fast-filter-off",
	"dreamina-seedance-2-0-mini-filter-off",
	seedance25Model,
}

var ChannelName = "doubao-video"

const videoInputRatioKey = "video_input"

type videoPriceKey struct {
	is1080p  bool
	is4k     bool
	hasVideo bool
}

// Selected overseas Seedance models are billed from the USD/M-token table when
// the async task completes. Legacy 260128 models keep the old ratio-only
// behavior because their table values use a different pricing basis.
var videoCompletionPriceModels = map[string]struct{}{
	"doubao-seedance-2-0-filter-off":        {},
	"doubao-seedance-2-0-fast-filter-off":   {},
	"dreamina-seedance-2-0-mini-filter-off": {},
	seedance25Model:                         {},
}

// videoPriceTable stores USD per 1M tokens for each output tier and whether
// the request includes video input. The zero key is the 480p/720p image/text
// input baseline used for pre-charge ratios.
var videoPriceTable = map[string]map[videoPriceKey]float64{
	"doubao-seedance-2-0-260128": {
		{hasVideo: false}:                46.0,
		{hasVideo: true}:                 28.0,
		{is1080p: true, hasVideo: false}: 51.0,
		{is1080p: true, hasVideo: true}:  31.0,
		{is4k: true, hasVideo: false}:    26.0,
		{is4k: true, hasVideo: true}:     16.0,
	},
	"doubao-seedance-2-0-fast-260128": {
		{hasVideo: false}: 37.0,
		{hasVideo: true}:  22.0,
	},
	"doubao-seedance-2-0-filter-off": {
		{hasVideo: false}:                7.0,
		{hasVideo: true}:                 4.3,
		{is1080p: true, hasVideo: false}: 7.7,
		{is1080p: true, hasVideo: true}:  4.7,
		{is4k: true, hasVideo: false}:    4.0,
		{is4k: true, hasVideo: true}:     2.4,
	},
	"doubao-seedance-2-0-fast-filter-off": {
		{hasVideo: false}: 5.6,
		{hasVideo: true}:  3.3,
	},
	"dreamina-seedance-2-0-mini-filter-off": {
		{hasVideo: false}: 3.5,
		{hasVideo: true}:  2.1,
	},
	seedance25Model: {
		{hasVideo: false}:                10.70,
		{hasVideo: true}:                 6.40,
		{is1080p: true, hasVideo: false}: 11.70,
		{is1080p: true, hasVideo: true}:  7.00,
	},
}

func GetVideoInputRatio(modelName, resolution string, hasVideo bool) (float64, bool) {
	prices, ok := videoPriceTable[modelName]
	if !ok {
		return 0, false
	}
	base := prices[videoPriceKey{}]
	if base <= 0 {
		return 0, false
	}

	res := strings.ToLower(strings.TrimSpace(resolution))
	price, ok := prices[videoPriceKey{is1080p: res == "1080p", is4k: res == "4k", hasVideo: hasVideo}]
	if !ok {
		return 1.0, true
	}
	return price / base, true
}

func getVideoCompletionUSDPerMTokens(modelName string, otherRatios map[string]float64) (float64, bool) {
	if _, ok := videoCompletionPriceModels[modelName]; !ok {
		return 0, false
	}
	prices, ok := videoPriceTable[modelName]
	if !ok {
		return 0, false
	}
	basePrice := prices[videoPriceKey{}]
	if basePrice <= 0 {
		return 0, false
	}

	priceRatio := 1.0
	if ratio := otherRatios[videoInputRatioKey]; ratio > 0 {
		priceRatio = ratio
	}
	return basePrice * priceRatio, true
}
