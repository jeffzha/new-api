package helper

import (
	"encoding/base64"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/gin-gonic/gin"
)

const MaxFunASRFlashDurationSeconds = 300
const funASRFormContextKey = "fun_asr_upload_form"
const funASRBodyContextKey = "fun_asr_request_body"
const funASRMetadataContextKey = "fun_asr_audio_metadata"

type FunASRAudioMetadata struct {
	Duration   float64
	Format     string
	MimeType   string
	SampleRate string
}

func InspectFunASRAudioRequest(c *gin.Context) (FunASRAudioMetadata, error) {
	metadata, _, _, err := inspectFunASRAudioRequest(c)
	return metadata, err
}

func inspectFunASRAudioRequest(c *gin.Context) (FunASRAudioMetadata, *multipart.Form, *multipart.FileHeader, error) {
	if c == nil || c.Request == nil {
		return FunASRAudioMetadata{}, nil, nil, fmt.Errorf("audio request is missing")
	}
	var form *multipart.Form
	var err error
	if cached, ok := c.Get(funASRMetadataContextKey); ok {
		metadata, valid := cached.(FunASRAudioMetadata)
		if valid {
			if formValue, exists := c.Get(funASRFormContextKey); exists {
				form, _ = formValue.(*multipart.Form)
			}
			if form != nil && len(form.File["file"]) == 1 {
				return metadata, form, form.File["file"][0], nil
			}
		}
	}
	if cached, ok := c.Get(funASRFormContextKey); ok {
		form, _ = cached.(*multipart.Form)
	}
	if form == nil {
		form = c.Request.MultipartForm
		if form == nil {
			form, err = common.ParseMultipartFormReusable(c)
		}
		if err != nil {
			return FunASRAudioMetadata{}, nil, nil, fmt.Errorf("invalid audio upload: %w", err)
		}
		c.Set(funASRFormContextKey, form)
	}
	files := form.File["file"]
	if len(files) != 1 {
		return FunASRAudioMetadata{}, nil, nil, fmt.Errorf("exactly one audio file is required")
	}
	fileCount := 0
	for _, fileHeaders := range form.File {
		fileCount += len(fileHeaders)
	}
	if fileCount != 1 {
		return FunASRAudioMetadata{}, nil, nil, fmt.Errorf("exactly one uploaded file is allowed")
	}
	fileHeader := files[0]
	if fileHeader.Size <= 0 {
		return FunASRAudioMetadata{}, nil, nil, fmt.Errorf("audio file is empty")
	}
	maxMB := constant.MaxRequestBodyMB
	if maxMB <= 0 {
		maxMB = 128
	}
	if fileHeader.Size > int64(maxMB)<<20 {
		return FunASRAudioMetadata{}, nil, nil, common.ErrRequestBodyTooLarge
	}
	ext := strings.TrimPrefix(strings.ToLower(filepath.Ext(fileHeader.Filename)), ".")
	mimeType, ok := funASRAudioFormat(ext)
	if !ok {
		return FunASRAudioMetadata{}, nil, nil, fmt.Errorf("unsupported audio format %q; use wav, mp3, flac, m4a, aac, ogg, opus or aiff", ext)
	}
	sampleRate := optionalMultipartValue(form.Value, "sample_rate")
	if sampleRate != "" {
		value, parseErr := strconv.Atoi(sampleRate)
		if parseErr != nil || value <= 0 || value > 192000 {
			return FunASRAudioMetadata{}, nil, nil, fmt.Errorf("sample_rate must be an integer between 1 and 192000")
		}
	}
	file, err := fileHeader.Open()
	if err != nil {
		return FunASRAudioMetadata{}, nil, nil, fmt.Errorf("open audio file: %w", err)
	}
	duration, err := common.GetAudioDuration(c.Request.Context(), file, "."+ext)
	_ = file.Close()
	if err != nil {
		return FunASRAudioMetadata{}, nil, nil, fmt.Errorf("read audio duration: %w", err)
	}
	if math.IsNaN(duration) || math.IsInf(duration, 0) || duration <= 0 || duration > MaxFunASRFlashDurationSeconds {
		return FunASRAudioMetadata{}, nil, nil, fmt.Errorf("Fun-ASR-Flash audio duration must be greater than 0 and at most %d seconds", MaxFunASRFlashDurationSeconds)
	}
	metadata := FunASRAudioMetadata{
		Duration: duration, Format: ext, MimeType: mimeType,
		SampleRate: sampleRate,
	}
	c.Set(funASRMetadataContextKey, metadata)
	return metadata, form, fileHeader, nil
}

type funASRReplayableBody struct {
	storage common.BodyStorage
	reader  io.ReadCloser
}

func (b *funASRReplayableBody) Read(p []byte) (int, error) { return b.reader.Read(p) }
func (b *funASRReplayableBody) Size() int64                { return b.storage.Size() }
func (b *funASRReplayableBody) NewReader() (io.ReadCloser, error) {
	return b.storage.NewReader()
}
func (b *funASRReplayableBody) Close() error {
	return b.reader.Close()
}
func (b *funASRReplayableBody) Release() error {
	readerErr := b.reader.Close()
	storageErr := b.storage.Close()
	if readerErr != nil {
		return readerErr
	}
	return storageErr
}

func ReleaseFunASRUpload(c *gin.Context) {
	if c == nil {
		return
	}
	if cached, ok := c.Get(funASRBodyContextKey); ok {
		if body, ok := cached.(*funASRReplayableBody); ok && body != nil {
			_ = body.Release()
		}
		c.Set(funASRBodyContextKey, nil)
	}
	if cached, ok := c.Get(funASRFormContextKey); ok {
		if form, ok := cached.(*multipart.Form); ok && form != nil {
			_ = form.RemoveAll()
		}
		c.Set(funASRFormContextKey, nil)
	}
	if c.Request != nil {
		c.Request.MultipartForm = nil
	}
	c.Set(funASRMetadataContextKey, nil)
}

func NewFunASRRequestBody(c *gin.Context, model string) (io.ReadCloser, error) {
	metadata, _, fileHeader, err := inspectFunASRAudioRequest(c)
	if err != nil {
		return nil, err
	}
	file, err := fileHeader.Open()
	if err != nil {
		return nil, fmt.Errorf("open audio file: %w", err)
	}
	defer file.Close()
	modelJSON, err := common.Marshal(model)
	if err != nil {
		return nil, err
	}
	prefix := []byte(fmt.Sprintf(`{"model":%s,"input":{"messages":[{"role":"user","content":[{"type":"input_audio","input_audio":{"data":"data:%s;base64,`, modelJSON, metadata.MimeType))
	parameters := map[string]string{"format": metadata.Format}
	if metadata.SampleRate != "" {
		parameters["sample_rate"] = metadata.SampleRate
	}
	parametersJSON, err := common.Marshal(parameters)
	if err != nil {
		return nil, err
	}
	suffix := []byte(fmt.Sprintf(`"}}]}]},"parameters":%s}`, parametersJSON))
	encodedLength := int64(base64.StdEncoding.EncodedLen(int(fileHeader.Size)))
	expectedLength := int64(len(prefix)+len(suffix)) + encodedLength
	maxMB := constant.MaxRequestBodyMB
	if maxMB <= 0 {
		maxMB = 128
	}
	maxBytes := int64(maxMB)<<20 + (int64(maxMB)<<20)/2 + 8192
	if expectedLength > maxBytes {
		return nil, common.ErrRequestBodyTooLarge
	}
	pipeReader, pipeWriter := io.Pipe()
	writeDone := make(chan error, 1)
	go func() {
		_, writeErr := pipeWriter.Write(prefix)
		if writeErr == nil {
			encoder := base64.NewEncoder(base64.StdEncoding, pipeWriter)
			_, writeErr = io.Copy(encoder, file)
			if closeErr := encoder.Close(); writeErr == nil {
				writeErr = closeErr
			}
		}
		if writeErr == nil {
			_, writeErr = pipeWriter.Write(suffix)
		}
		_ = pipeWriter.CloseWithError(writeErr)
		writeDone <- writeErr
	}()
	storage, storageErr := common.CreateBodyStorageFromReader(pipeReader, expectedLength, maxBytes)
	_ = pipeReader.CloseWithError(storageErr)
	writeErr := <-writeDone
	if storageErr != nil {
		return nil, fmt.Errorf("encode audio request: %w", storageErr)
	}
	if writeErr != nil {
		_ = storage.Close()
		return nil, fmt.Errorf("encode audio request: %w", writeErr)
	}
	reader, err := storage.NewReader()
	if err != nil {
		_ = storage.Close()
		return nil, err
	}
	body := &funASRReplayableBody{storage: storage, reader: reader}
	c.Set(funASRBodyContextKey, body)
	return body, nil
}

func IsFunASRModel(model string) bool {
	return strings.EqualFold(strings.TrimSpace(model), "fun-asr-flash-2026-06-15")
}

func IsFunASRContentType(contentType string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(contentType)), "multipart/form-data")
}

func optionalMultipartValue(values map[string][]string, name string) string {
	if len(values[name]) == 0 {
		return ""
	}
	return strings.TrimSpace(values[name][0])
}

func funASRAudioFormat(ext string) (string, bool) {
	switch ext {
	case "wav":
		return "audio/wav", true
	case "mp3":
		return "audio/mpeg", true
	case "flac":
		return "audio/flac", true
	case "m4a", "mp4":
		return "audio/mp4", true
	case "aac":
		return "audio/aac", true
	case "ogg", "oga":
		return "audio/ogg", true
	case "opus":
		return "audio/opus", true
	case "aiff", "aif", "aifc":
		return "audio/aiff", true
	default:
		return "", false
	}
}
