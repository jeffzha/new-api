package dto

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"

	kitutil "github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
	"github.com/QuantumNous/new-api/relaykit/types"
)

// MaxImageN caps the image generation count. Without this bound a huge or
// wrapped-negative n overflows quota calculation into a negative charge.
const MaxImageN = 128

// MaxReferenceImageN caps the number of reference images counted for the
// additive per-reference-image billing surcharge. Mirrors the generation-count
// bound so a huge or wrapped array cannot overflow the surcharge computation.
const MaxReferenceImageN = 128

// ImageBillingParameters contains only the provider scalars parsed by request
// validation. Keep this separate from the complete provider request payload.
type ImageBillingParameters struct {
	N            *uint `json:"n,omitempty"`
	PromptExtend *bool `json:"prompt_extend,omitempty"`
}

type ImageRequest struct {
	Model             string          `json:"model"`
	Prompt            string          `json:"prompt" binding:"required"`
	N                 *uint           `json:"n,omitempty"`
	Size              string          `json:"size,omitempty"`
	Quality           string          `json:"quality,omitempty"`
	ResponseFormat    string          `json:"response_format,omitempty"`
	Style             json.RawMessage `json:"style,omitempty"`
	User              json.RawMessage `json:"user,omitempty"`
	ExtraFields       json.RawMessage `json:"extra_fields,omitempty"`
	Background        json.RawMessage `json:"background,omitempty"`
	Moderation        json.RawMessage `json:"moderation,omitempty"`
	OutputFormat      json.RawMessage `json:"output_format,omitempty"`
	OutputCompression json.RawMessage `json:"output_compression,omitempty"`
	PartialImages     json.RawMessage `json:"partial_images,omitempty"`
	Stream            *bool           `json:"stream,omitempty"`
	Images            json.RawMessage `json:"images,omitempty"`
	Mask              json.RawMessage `json:"mask,omitempty"`
	InputFidelity     json.RawMessage `json:"input_fidelity,omitempty"`
	Watermark         *bool           `json:"watermark,omitempty"`
	// zhipu 4v
	WatermarkEnabled json.RawMessage `json:"watermark_enabled,omitempty"`
	UserId           json.RawMessage `json:"user_id,omitempty"`
	Image            json.RawMessage `json:"image,omitempty"`
	// 用匿名参数接收额外参数
	Extra             map[string]json.RawMessage `json:"-"`
	BillingParameters *ImageBillingParameters    `json:"-"`
}

// ImageCount resolves the validated request quantity. Top-level zero retains
// its legacy default of one; an explicit provider count must be positive.
func (i *ImageRequest) ImageCount(useProviderParameters bool) (int, error) {
	n := uint(1)
	if i.N != nil && *i.N != 0 {
		n = *i.N
	}
	if n > MaxImageN {
		return 0, fmt.Errorf("n must be an integer between 1 and %d", MaxImageN)
	}
	if parameters := i.BillingParameters; parameters != nil && parameters.N != nil {
		if *parameters.N > MaxImageN || useProviderParameters && *parameters.N == 0 {
			return 0, fmt.Errorf("parameters.n must be an integer between 1 and %d", MaxImageN)
		}
		if useProviderParameters {
			n = *parameters.N
		}
	}
	return int(n), nil
}

// ReferenceImageCount returns the number of reference images attached to an
// image-to-image request. It detects the request mode:
//   - Ali-native mode (a provider input object present): counts reference images
//     from the input, covering both wan-style input.images arrays and ali
//     message-style input.messages[].content[].image entries.
//   - OpenAI-compatible mode: counts the top-level images array length.
//
// It is 0 when no reference images are supplied. A malformed payload is
// rejected so billing never silently skips a surcharge.
func (i *ImageRequest) ReferenceImageCount() (int, error) {
	if input, ok := i.Extra["input"]; ok && len(input) > 0 {
		return countAliInputReferenceImages(input)
	}
	if len(i.Images) == 0 {
		return 0, nil
	}
	var refs []string
	if err := json.Unmarshal(i.Images, &refs); err != nil {
		return 0, fmt.Errorf("images must be an array of reference image URLs")
	}
	if err := validateReferenceImageCount(len(refs)); err != nil {
		return 0, err
	}
	return len(refs), nil
}

// aliImageInputShape captures the provider input fields that can carry
// reference images, independent of the channel implementation package.
type aliImageInputShape struct {
	Images   []string `json:"images"`
	Messages []struct {
		Content json.RawMessage `json:"content"`
	} `json:"messages"`
}

func countAliInputReferenceImages(raw json.RawMessage) (int, error) {
	var in aliImageInputShape
	if err := json.Unmarshal(raw, &in); err != nil {
		return 0, fmt.Errorf("invalid ali input for reference image billing: %w", err)
	}
	count := len(in.Images)
	for _, m := range in.Messages {
		// content may be a plain string (text-only) or an array of media parts.
		var contents []struct {
			Image string `json:"image"`
		}
		if err := json.Unmarshal(m.Content, &contents); err != nil {
			continue
		}
		for _, c := range contents {
			if c.Image != "" {
				count++
			}
		}
	}
	if err := validateReferenceImageCount(count); err != nil {
		return 0, err
	}
	return count, nil
}

func validateReferenceImageCount(n int) error {
	if n > MaxReferenceImageN {
		return fmt.Errorf("reference image count must not exceed %d", MaxReferenceImageN)
	}
	return nil
}

func (i *ImageRequest) UnmarshalJSON(data []byte) error {
	// 先解析成 map[string]interface{}
	var rawMap map[string]json.RawMessage
	if err := kitutil.Unmarshal(data, &rawMap); err != nil {
		return err
	}

	// 用 struct tag 获取所有已定义字段名
	knownFields := GetJSONFieldNames(reflect.TypeFor[ImageRequest]())

	// 再正常解析已定义字段
	type Alias ImageRequest
	var known Alias
	if err := kitutil.Unmarshal(data, &known); err != nil {
		return err
	}
	*i = ImageRequest(known)

	// 提取多余字段
	i.Extra = make(map[string]json.RawMessage)
	for k, v := range rawMap {
		if _, ok := knownFields[k]; !ok {
			i.Extra[k] = v
		}
	}
	return nil
}

// 序列化时需要重新把字段平铺
func (r ImageRequest) MarshalJSON() ([]byte, error) {
	// 将已定义字段转为 map
	type Alias ImageRequest
	alias := Alias(r)
	base, err := kitutil.Marshal(alias)
	if err != nil {
		return nil, err
	}

	var baseMap map[string]json.RawMessage
	if err := kitutil.Unmarshal(base, &baseMap); err != nil {
		return nil, err
	}

	// 不能合并ExtraFields！！！！！！！！
	// 合并 ExtraFields
	//for k, v := range r.Extra {
	//	if _, exists := baseMap[k]; !exists {
	//		baseMap[k] = v
	//	}
	//}

	return kitutil.Marshal(baseMap)
}

func GetJSONFieldNames(t reflect.Type) map[string]struct{} {
	fields := make(map[string]struct{})
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)

		// 跳过匿名字段（例如 ExtraFields）
		if field.Anonymous {
			continue
		}

		tag := field.Tag.Get("json")
		if tag == "-" || tag == "" {
			continue
		}

		// 取逗号前字段名（排除 omitempty 等）
		name := tag
		if commaIdx := indexComma(tag); commaIdx != -1 {
			name = tag[:commaIdx]
		}
		fields[name] = struct{}{}
	}
	return fields
}

func indexComma(s string) int {
	for i := 0; i < len(s); i++ {
		if s[i] == ',' {
			return i
		}
	}
	return -1
}

func (i *ImageRequest) GetTokenCountMeta() *types.TokenCountMeta {
	imageN := uint(1)
	if i.N != nil && *i.N > 0 {
		imageN = *i.N
	}

	// Keep n separate from ImagePriceRatio so size/quality and count remain
	// independent billing dimensions. Fixed-price pre-consume stores this on
	// PriceData, and image settlement reuses or replaces the same "n" ratio.
	return &types.TokenCountMeta{
		CombineText:     i.Prompt,
		MaxTokens:       1584,
		ImagePriceRatio: i.legacyDallePriceRatio(),
		BillingRatios:   map[string]float64{"n": float64(imageN)},
	}
}

func (i *ImageRequest) IsStream(c *http.Request) bool {
	return i.Stream != nil && *i.Stream
}

func (i *ImageRequest) SetModelName(modelName string) {
	if modelName != "" {
		i.Model = modelName
	}
}

type ImageResponse struct {
	Data     []ImageData     `json:"data"`
	Created  int64           `json:"created"`
	Metadata json.RawMessage `json:"metadata,omitempty"`
}
type ImageData struct {
	Url           string `json:"url"`
	B64Json       string `json:"b64_json"`
	RevisedPrompt string `json:"revised_prompt"`
}
