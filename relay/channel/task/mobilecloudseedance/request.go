package mobilecloudseedance

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/relay/channel/task/taskcommon"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/samber/lo"
)

func convertRequest(req *relaycommon.TaskSubmitReq) (*requestPayload, error) {
	payload := requestPayload{
		Model:   ModelName,
		Content: make([]contentItem, 0, len(req.Images)+1),
	}
	if err := taskcommon.UnmarshalMetadata(req.Metadata, &payload); err != nil {
		return nil, err
	}

	if len(req.Content) > 0 {
		contentData, err := common.Marshal(req.Content)
		if err != nil {
			return nil, fmt.Errorf("marshal content: %w", err)
		}
		if err := common.Unmarshal(contentData, &payload.Content); err != nil {
			return nil, fmt.Errorf("unmarshal content: %w", err)
		}
	}
	for _, image := range req.Images {
		payload.Content = append(payload.Content, contentItem{
			Type:     "image_url",
			ImageURL: &mediaURL{URL: strings.TrimSpace(image)},
		})
	}

	payload.Content = lo.Reject(payload.Content, func(item contentItem, _ int) bool {
		return item.Type == "text"
	})
	payload.Content = append(payload.Content, contentItem{
		Type: "text",
		Text: strings.TrimSpace(req.Prompt),
	})

	if strings.TrimSpace(req.Resolution) != "" {
		payload.Resolution = strings.ToLower(strings.TrimSpace(req.Resolution))
	}
	if strings.TrimSpace(req.Ratio) != "" {
		payload.Ratio = strings.TrimSpace(req.Ratio)
	}
	if req.Duration != 0 {
		payload.Duration = lo.ToPtr(dto.IntValue(req.Duration))
	} else if strings.TrimSpace(req.Seconds) != "" {
		duration, err := strconv.Atoi(strings.TrimSpace(req.Seconds))
		if err != nil {
			return nil, fmt.Errorf("seconds must be an integer")
		}
		payload.Duration = lo.ToPtr(dto.IntValue(duration))
	}
	if req.GenerateAudio != nil {
		payload.GenerateAudio = lo.ToPtr(dto.BoolValue(*req.GenerateAudio))
	}
	if payload.Resolution == "" {
		payload.Resolution = "720p"
	}
	if payload.Ratio == "" {
		payload.Ratio = "adaptive"
	}
	if payload.Duration == nil {
		payload.Duration = lo.ToPtr(dto.IntValue(5))
	}

	return &payload, validateRequestPayload(payload)
}

func validateRequestPayload(payload requestPayload) error {
	switch payload.Resolution {
	case "480p", "720p", "1080p":
	default:
		return fmt.Errorf("resolution must be 480p, 720p, or 1080p")
	}

	if payload.Duration != nil {
		duration := int(*payload.Duration)
		if duration != -1 && (duration < 4 || duration > 15) {
			return fmt.Errorf("duration must be -1 or between 4 and 15 seconds")
		}
	}
	if payload.Frames != nil {
		frames := int(*payload.Frames)
		if frames <= 0 || frames > relaycommon.MaxTaskDurationSeconds*24 {
			return fmt.Errorf("frames must be between 1 and %d", relaycommon.MaxTaskDurationSeconds*24)
		}
	}

	videoCount := 0
	imageCount := 0
	for i, item := range payload.Content {
		switch item.Type {
		case "text":
			if strings.TrimSpace(item.Text) == "" {
				return fmt.Errorf("content[%d].text is required", i)
			}
		case "image_url":
			imageCount++
			if item.ImageURL == nil || strings.TrimSpace(item.ImageURL.URL) == "" {
				return fmt.Errorf("content[%d].image_url.url is required", i)
			}
		case "video_url":
			videoCount++
			if item.VideoURL == nil || strings.TrimSpace(item.VideoURL.URL) == "" {
				return fmt.Errorf("content[%d].video_url.url is required", i)
			}
		case "audio_url":
			if item.AudioURL == nil || strings.TrimSpace(item.AudioURL.URL) == "" {
				return fmt.Errorf("content[%d].audio_url.url is required", i)
			}
		default:
			return fmt.Errorf("content[%d].type %q is unsupported", i, item.Type)
		}
	}
	if imageCount > 9 {
		return fmt.Errorf("content supports at most 9 image inputs")
	}
	if videoCount > 3 {
		return fmt.Errorf("content supports at most 3 video inputs")
	}
	return nil
}

func videoInputCount(payload *requestPayload) int64 {
	var count int64
	for _, item := range payload.Content {
		if item.Type == "video_url" {
			count++
		}
	}
	return count
}
