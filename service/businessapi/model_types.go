package businessapi

import (
	"slices"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
)

func ValidModelType(value string) bool {
	switch value {
	case "llm", "rerank", "embedding", "image", "video", "speech2text", "tts", "audio":
		return true
	default:
		return false
	}
}

// ModelType uses the catalog's supported endpoints for custom names and known
// model families where a compatible channel only advertises its generic API.
// A vision-capable LLM remains llm; image/video mean generation tasks.
func ModelType(name string, endpoints []constant.EndpointType) string {
	name = strings.ToLower(name)
	switch {
	case slices.Contains(endpoints, constant.EndpointTypeJinaRerank), strings.Contains(name, "rerank"):
		return "rerank"
	case slices.Contains(endpoints, constant.EndpointTypeEmbeddings), strings.Contains(name, "embedding"), strings.Contains(name, "embed-"), strings.HasPrefix(name, "bge-"), strings.HasPrefix(name, "text2vec"):
		return "embedding"
	case slices.Contains(endpoints, constant.EndpointTypeImageGeneration), common.IsImageGenerationModel(name), strings.Contains(name, "seedream"), strings.Contains(name, "nano-banana"), strings.Contains(name, "midjourney"), strings.HasPrefix(name, "mj_"), strings.Contains(name, "-image"):
		return "image"
	case slices.Contains(endpoints, constant.EndpointTypeOpenAIVideo), common.IsVideoGenerationModel(name), strings.Contains(name, "video"), strings.Contains(name, "-t2v"), strings.Contains(name, "-i2v"), strings.Contains(name, "-r2v"), strings.HasPrefix(name, "kling"), strings.HasPrefix(name, "happyhorse"):
		return "video"
	case strings.Contains(name, "whisper"), strings.Contains(name, "asr"), strings.Contains(name, "transcri"), strings.Contains(name, "speech2text"), strings.Contains(name, "speech-to-text"):
		return "speech2text"
	case strings.Contains(name, "tts"), strings.Contains(name, "text-to-speech"), strings.HasPrefix(name, "speech-"):
		return "tts"
	case strings.Contains(name, "audio"), strings.Contains(name, "suno"), strings.Contains(name, "music"), strings.Contains(name, "speech"):
		return "audio"
	default:
		return "llm"
	}
}
