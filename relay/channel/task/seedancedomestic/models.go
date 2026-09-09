package seedancedomestic

import "encoding/json"

type generateRequest struct {
	Content     []map[string]interface{} `json:"content"`
	AudioStatus int                      `json:"audio_status"`
	Resolution  string                   `json:"resolution"`
	Ratio       string                   `json:"ratio"`
	Dur         int                      `json:"dur"`
}

type metadataRequest struct {
	Content       []map[string]interface{} `json:"content,omitempty"`
	AudioStatus   *int                     `json:"audio_status,omitempty"`
	GenerateAudio *bool                    `json:"generate_audio,omitempty"`
	Resolution    string                   `json:"resolution,omitempty"`
	Ratio         string                   `json:"ratio,omitempty"`
	Dur           *int                     `json:"dur,omitempty"`
}

type upstreamEnvelope[T any] struct {
	State int `json:"state"`
	Data  T   `json:"data"`
	Error any `json:"error"`
}

type generateResponse struct {
	ID json.RawMessage `json:"id"`
}

type generateInfoResponse struct {
	ID        json.RawMessage `json:"id"`
	Status    int             `json:"status"`
	VideoURL  string          `json:"video_url"`
	Message   any             `json:"message"`
	CreatedAt string          `json:"created_at"`
}

type billListRequest struct {
	ExpenseDateStart string `json:"expense_date_start"`
	ExpenseDateEnd   string `json:"expense_date_end"`
	Page             int    `json:"page"`
	Size             int    `json:"size"`
}

type billListResponse struct {
	Total json.RawMessage `json:"total"`
	Page  json.RawMessage `json:"page"`
	Size  json.RawMessage `json:"size"`
	List  []billItem      `json:"list"`
}

type billItem struct {
	ID            json.RawMessage `json:"id"`
	Resolution    string          `json:"resolution"`
	Ratio         string          `json:"ratio"`
	Dur           json.RawMessage `json:"dur"`
	ExpenseTime   string          `json:"expense_time"`
	TotalTokens   json.RawMessage `json:"total_tokens"`
	Price         json.RawMessage `json:"price"`
	OriginalPrice json.RawMessage `json:"original_price"`
	Discount      json.RawMessage `json:"discount"`
	AmountPaid    json.RawMessage `json:"amount_paid"`
}
