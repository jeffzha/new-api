package helper

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func funASRTestWAV(sampleRate uint32, seconds int) []byte {
	dataSize := int(sampleRate) * seconds * 2
	var body bytes.Buffer
	body.WriteString("RIFF")
	_ = binary.Write(&body, binary.LittleEndian, uint32(36+dataSize))
	body.WriteString("WAVEfmt ")
	_ = binary.Write(&body, binary.LittleEndian, uint32(16))
	_ = binary.Write(&body, binary.LittleEndian, uint16(1))
	_ = binary.Write(&body, binary.LittleEndian, uint16(1))
	_ = binary.Write(&body, binary.LittleEndian, sampleRate)
	_ = binary.Write(&body, binary.LittleEndian, sampleRate*2)
	_ = binary.Write(&body, binary.LittleEndian, uint16(2))
	_ = binary.Write(&body, binary.LittleEndian, uint16(16))
	body.WriteString("data")
	_ = binary.Write(&body, binary.LittleEndian, uint32(dataSize))
	body.Write(make([]byte, dataSize))
	return body.Bytes()
}

func funASRMultipartRequest(t *testing.T, filename string, audio []byte, sampleRate string) *gin.Context {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	require.NoError(t, writer.WriteField("model", "fun-asr-flash-2026-06-15"))
	if sampleRate != "" {
		require.NoError(t, writer.WriteField("sample_rate", sampleRate))
	}
	part, err := writer.CreateFormFile("file", filename)
	require.NoError(t, err)
	_, err = part.Write(audio)
	require.NoError(t, err)
	require.NoError(t, writer.Close())
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/audio/transcriptions", &body)
	c.Request.Header.Set("Content-Type", writer.FormDataContentType())
	return c
}

func TestFunASRMultipartConvertsToDashScopeRequest(t *testing.T) {
	audio := funASRTestWAV(16000, 1)
	c := funASRMultipartRequest(t, "sample.wav", audio, "16000")
	request, err := GetAndValidAudioRequest(c, relayconstant.RelayModeAudioTranscription)
	require.NoError(t, err)
	require.Equal(t, "fun-asr-flash-2026-06-15", request.Model)

	metadata, err := InspectFunASRAudioRequest(c)
	require.NoError(t, err)
	require.InDelta(t, 1, metadata.Duration, 0.001)
	require.Equal(t, "wav", metadata.Format)

	body, err := NewFunASRRequestBody(c, request.Model)
	require.NoError(t, err)
	payload, err := io.ReadAll(body)
	require.NoError(t, err)
	var upstream struct {
		Model string `json:"model"`
		Input struct {
			Messages []struct {
				Content []struct {
					InputAudio struct {
						Data string `json:"data"`
					} `json:"input_audio"`
				} `json:"content"`
			} `json:"messages"`
		} `json:"input"`
		Parameters map[string]string `json:"parameters"`
	}
	require.NoError(t, common.Unmarshal(payload, &upstream))
	require.Equal(t, request.Model, upstream.Model)
	require.Equal(t, "wav", upstream.Parameters["format"])
	require.Equal(t, "16000", upstream.Parameters["sample_rate"])
	require.Len(t, upstream.Input.Messages, 1)
	require.Len(t, upstream.Input.Messages[0].Content, 1)
	encoded := strings.TrimPrefix(upstream.Input.Messages[0].Content[0].InputAudio.Data, "data:audio/wav;base64,")
	decoded, err := base64.StdEncoding.DecodeString(encoded)
	require.NoError(t, err)
	require.Equal(t, audio, decoded)
	ReleaseFunASRUpload(c)
}

func TestFunASRRejectsInvalidUploadBeforeUpstream(t *testing.T) {
	for _, tc := range []struct {
		name       string
		filename   string
		audio      []byte
		sampleRate string
		contains   string
	}{
		{"unsupported format", "sample.txt", []byte("not audio"), "", "unsupported audio format"},
		{"invalid sample rate", "sample.wav", funASRTestWAV(16000, 1), "abc", "sample_rate"},
		{"excess duration", "sample.wav", funASRTestWAV(16000, 301), "", "at most 300 seconds"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := funASRMultipartRequest(t, tc.filename, tc.audio, tc.sampleRate)
			_, err := InspectFunASRAudioRequest(c)
			require.ErrorContains(t, err, tc.contains)
			ReleaseFunASRUpload(c)
		})
	}
}
