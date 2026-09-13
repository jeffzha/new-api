package openaiseedance

// requestMetadata mirrors the OpenAI-format video metadata accepted from the
// client. The laomandi/seedance convention carries duration/resolution/ratio/
// audio in the metadata object, so these are read with metadata taking
// precedence over the equivalent top-level TaskSubmitReq fields.
type requestMetadata struct {
	Duration      *int   `json:"duration"`
	Dur           *int   `json:"dur"`
	Resolution    string `json:"resolution"`
	Ratio         string `json:"ratio"`
	GenerateAudio *bool  `json:"generate_audio"`
	AudioStatus   *int   `json:"audio_status"`
}

// generateRequest is the normalized OpenAI-format video generate request that
// is forwarded upstream. The client speaks OpenAI /v1/video/generations, so
// the prompt is sent as a top-level "prompt" field (not volcengine content[]).
// Image carries an optional single reference image URL (image-to-video), read
// from any OpenAI-format field (image/images/content[].image_url) and forwarded
// as the top-level OpenAI "image" field that the upstream seedance gateway
// understands.
type generateRequest struct {
	Prompt      string `json:"prompt"`
	Model       string `json:"model"`
	Image       string `json:"image,omitempty"`
	Resolution  string `json:"resolution"`
	Ratio       string `json:"ratio"`
	Duration    int    `json:"duration"`
	AudioStatus int    `json:"audio_status"`
}

// responseTask mirrors the OpenAI-compatible video task status response that
// OpenAI-compatible seedance upstreams (e.g. vedioapi.laomandi.com) return for
// both submit and status-polling.
type responseTask struct {
	ID       string `json:"id"`
	TaskID   string `json:"task_id"`
	Status   string `json:"status"`
	Progress int    `json:"progress"`
	Content  struct {
		VideoURL string `json:"video_url"`
	} `json:"content"`
	Usage struct {
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
	CreatedAt int64 `json:"created_at"`
	Error     *struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}
