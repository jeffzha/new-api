package modelhealth

import (
	"fmt"
	"mime"
	"net/http"
	"regexp"
	"strconv"
	"strings"

	"github.com/QuantumNous/new-api/common"
)

var evidenceRange = regexp.MustCompile(`^bytes[ =][0-9]{0,19}-[0-9]{0,19}(/[0-9*]{1,19})?$`)

// Only explicitly useful wire values are retained. Do not serialize raw headers,
// opaque URLs, non-JSON text, credentials or media bytes, even temporarily.
func evidenceHeaders(header http.Header, length int64) map[string]string {
	out := map[string]string{}
	if raw := header.Get("Content-Type"); raw != "" {
		value, _, _ := mime.ParseMediaType(raw)
		switch value {
		case "application/json", "application/problem+json", "application/octet-stream", "multipart/form-data", "text/plain", "text/html", "video/mp4", "video/webm", "image/png", "image/jpeg", "image/webp", "audio/wav", "audio/mpeg":
			out["Content-Type"] = value
		default:
			out["Content-Type"] = "[other media type redacted]"
		}
	}
	if length >= 0 {
		out["Content-Length"] = strconv.FormatInt(length, 10)
	}
	for _, name := range []string{"Range", "Content-Range"} {
		if raw := header.Get(name); evidenceRange.MatchString(raw) {
			out[name] = raw
		}
	}
	if value, err := strconv.ParseUint(header.Get("Retry-After"), 10, 32); err == nil {
		out["Retry-After"] = strconv.FormatUint(value, 10)
	}
	if header.Get("Anthropic-Version") == "2023-06-01" {
		out["Anthropic-Version"] = "2023-06-01"
	}
	for _, name := range []string{"Authorization", "X-Api-Key", "X-Goog-Api-Key", "Cookie", "Set-Cookie", "Location"} {
		if header.Get(name) != "" {
			out[name] = "[redacted]"
		}
	}
	return out
}

func hasMP4Header(data []byte) bool { return len(data) >= 12 && string(data[4:8]) == "ftyp" }

func evidenceBody(data []byte, err error) any {
	if err != nil {
		return map[string]any{"body": "[not fully captured: read error or size limit]"}
	}
	result := map[string]any{"body": "[non-JSON body omitted]", "captured_bytes": len(data)}
	if hasMP4Header(data) {
		result["file_signature"] = "MP4 ftyp"
		switch brand := string(data[8:12]); brand {
		case "isom", "iso2", "mp41", "mp42", "avc1":
			result["major_brand"] = brand
		}
	}
	return result
}

func writeEvidenceJSON(b *strings.Builder, title string, value any) {
	if value == nil {
		fmt.Fprintf(b, "\n%s：无或未采集。\n", title)
		return
	}
	data, err := common.Marshal(value)
	if err != nil {
		fmt.Fprintf(b, "\n%s：未采集。\n", title)
		return
	}
	fmt.Fprintf(b, "\n%s（已脱敏）：\n\n```json\n%s\n```\n", title, data)
}
