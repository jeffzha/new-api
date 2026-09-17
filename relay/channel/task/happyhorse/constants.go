package happyhorse

const (
	providerName = "happyhorse"
	submitPath   = "/api/v1/services/aigc/video-generation/video-synthesis"
	modelT2V     = "happyhorse-1.1-t2v"
	modelI2V     = "happyhorse-1.1-i2v"
	modelR2V     = "happyhorse-1.1-r2v"
	modelEdit    = "happyhorse-1.0-video-edit"
	modelEdit11  = "happyhorse-1.1-video-edit"
	modelAlias   = "HappyHorse1.1"
)

var ModelList = []string{modelT2V, modelI2V, modelR2V, modelEdit, modelEdit11}
var ChannelName = "HappyHorse"
var resolutions = map[string]bool{"480P": true, "720P": true, "1080P": true}
var ratios = map[string]bool{"16:9": true, "9:16": true, "1:1": true, "4:3": true, "3:4": true, "4:5": true, "5:4": true, "9:21": true, "21:9": true}
