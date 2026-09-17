package happyhorse

import "github.com/QuantumNous/new-api/common"

type request struct {
	Model      string      `json:"model"`
	Input      input       `json:"input"`
	Parameters *parameters `json:"parameters,omitempty"`
}
type input struct {
	Prompt string  `json:"prompt,omitempty"`
	Media  []media `json:"media,omitempty"`
}

type media struct {
	Type string `json:"type"`
	URL  string `json:"url"`
}
type parameters struct {
	Resolution   string `json:"resolution,omitempty"`
	Ratio        string `json:"ratio,omitempty"`
	Duration     *int   `json:"duration,omitempty"`
	Watermark    *bool  `json:"watermark,omitempty"`
	Seed         *int   `json:"seed,omitempty"`
	AudioSetting string `json:"audio_setting,omitempty"`
}
type response struct {
	RequestID string `json:"request_id,omitempty"`
	Code      string `json:"code,omitempty"`
	Message   string `json:"message,omitempty"`
	Output    output `json:"output"`
	Usage     *usage `json:"usage,omitempty"`
}
type output struct {
	TaskID     string `json:"task_id"`
	TaskStatus string `json:"task_status"`
	Resolution string `json:"resolution,omitempty"`
	VideoURL   string `json:"video_url,omitempty"`
	Code       string `json:"code,omitempty"`
	Message    string `json:"message,omitempty"`
}
type usage struct {
	Duration            float64 `json:"duration,omitempty"`
	InputVideoDuration  float64 `json:"input_video_duration,omitempty"`
	OutputVideoDuration float64 `json:"output_video_duration,omitempty"`
	Resolution          string  `json:"resolution,omitempty"`
}

func decodeResponse(body []byte) (response, error) {
	var result response
	err := common.Unmarshal(body, &result)
	return result, err
}
