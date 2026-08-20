package mobilecloudseedance

import "github.com/QuantumNous/new-api/relaykit/dto"

type mediaURL struct {
	URL string `json:"url"`
}

type contentItem struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *mediaURL `json:"image_url,omitempty"`
	VideoURL *mediaURL `json:"video_url,omitempty"`
	AudioURL *mediaURL `json:"audio_url,omitempty"`
	Role     string    `json:"role,omitempty"`
}

type requestPayload struct {
	Model                 string         `json:"model"`
	Content               []contentItem  `json:"content"`
	CallbackURL           string         `json:"callback_url,omitempty"`
	ReturnLastFrame       *dto.BoolValue `json:"return_last_frame,omitempty"`
	ServiceTier           string         `json:"service_tier,omitempty"`
	ExecutionExpiresAfter *dto.IntValue  `json:"execution_expires_after,omitempty"`
	GenerateAudio         *dto.BoolValue `json:"generate_audio,omitempty"`
	Draft                 *dto.BoolValue `json:"draft,omitempty"`
	Tools                 []tool         `json:"tools,omitempty"`
	SafetyIdentifier      string         `json:"safety_identifier,omitempty"`
	Priority              *dto.IntValue  `json:"priority,omitempty"`
	Resolution            string         `json:"resolution,omitempty"`
	Ratio                 string         `json:"ratio,omitempty"`
	Duration              *dto.IntValue  `json:"duration,omitempty"`
	Frames                *dto.IntValue  `json:"frames,omitempty"`
	Seed                  *dto.IntValue  `json:"seed,omitempty"`
	CameraFixed           *dto.BoolValue `json:"camera_fixed,omitempty"`
	Watermark             *dto.BoolValue `json:"watermark,omitempty"`
}

type tool struct {
	Type string `json:"type"`
}

type createTaskResponse struct {
	ID string `json:"id"`
}

type taskResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Status  string `json:"status"`
	Content struct {
		VideoURL     string `json:"video_url"`
		LastFrameURL string `json:"last_frame_url"`
	} `json:"content"`
	Seed            dto.IntValue `json:"seed"`
	Resolution      string       `json:"resolution"`
	Duration        int          `json:"duration"`
	Ratio           string       `json:"ratio"`
	FramesPerSecond int          `json:"framespersecond"`
	ServiceTier     string       `json:"service_tier"`
	Usage           struct {
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
	CreatedAt int64 `json:"created_at"`
	UpdatedAt int64 `json:"updated_at"`
}
