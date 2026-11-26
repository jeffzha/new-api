package modelhealth

import (
	"bytes"
	_ "embed"
	"errors"
	"mime/multipart"
	"net/textproto"
	"slices"

	"github.com/QuantumNous/new-api/common"
)

// Synthesized locally with Windows Zira: “Hello. This is a model health check.”
// 16 kHz mono PCM, no customer content or remote download. The fixed clip is
// shorter than the five-second admission estimate.
//
//go:embed fixtures/speech-hello-v1.wav
var speechHello []byte

func probePayload(p Template) ([]byte, string, error) {
	if p.InputFixture == "" {
		body, err := common.Marshal(p.Body)
		return body, "application/json", err
	}
	if p.InputFixture != "speech-hello-v1" || p.Endpoint != "audio_transcription" || p.Path != "/v1/audio/transcriptions" {
		return nil, "", errors.New("unsupported probe input fixture")
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	keys := make([]string, 0, len(p.Body))
	for key := range p.Body {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		value, ok := p.Body[key].(string)
		if !ok || key != "model" && key != "response_format" && key != "language" {
			return nil, "", errors.New("unsupported audio probe field")
		}
		if err := writer.WriteField(key, value); err != nil {
			return nil, "", err
		}
	}
	part, err := writer.CreatePart(textproto.MIMEHeader{"Content-Disposition": {`form-data; name="file"; filename="speech-hello-v1.wav"`}, "Content-Type": {"audio/wav"}})
	if err != nil {
		return nil, "", err
	}
	if _, err = part.Write(speechHello); err != nil {
		return nil, "", err
	}
	if err = writer.Close(); err != nil {
		return nil, "", err
	}
	return body.Bytes(), writer.FormDataContentType(), nil
}
