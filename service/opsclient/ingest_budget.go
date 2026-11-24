package opsclient

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// RetryAfter accepts both forms from RFC 9110. Invalid or past dates do not
// shorten a caller's existing backoff; callers apply their own minimum.
func RetryAfter(value string, at time.Time) time.Duration {
	seconds, err := strconv.ParseInt(value, 10, 64)
	if err == nil && seconds > 0 {
		return time.Duration(min(seconds, int64((1<<63-1)/time.Second))) * time.Second
	}
	if until, err := http.ParseTime(value); err == nil && until.After(at) {
		return until.Sub(at)
	}
	return 0
}

// The station ingest allowance is shared by usage, funding and metadata.
// Independent reserved lanes total 108/minute, below the server default 120.
// A blocked metadata/funding lane cannot spend the usage lane's allowance.
type IngestLane string

const (
	IngestUsage    IngestLane = "usage"
	IngestFunding  IngestLane = "funding"
	IngestMetadata IngestLane = "metadata"
)

func ingestKey(base, key string, lane IngestLane) string {
	return fmt.Sprintf("ops-ingest-budget:v1:%x:%s", sha256.Sum256([]byte(strings.TrimRight(base, "/")+"\n"+key)), lane)
}

// ReserveIngest never waits inside a model request. Only background senders
// use this gate; deferred events remain in their existing durable outboxes.
func ReserveIngest(ctx context.Context, base, key string, lane IngestLane) error {
	if os.Getenv("OPS_INGEST_BUDGET_ENABLED") != "true" {
		return nil
	}
	limit := 0
	switch lane {
	case IngestUsage:
		limit = 78
	case IngestFunding:
		limit = 18
	case IngestMetadata:
		limit = 12
	default:
		return &ReadError{Code: "invalid_ingest_lane"}
	}
	return reserveWindow(ctx, ingestKey(base, key, lane), limit)
}

// A server-wide ingest throttle applies to every lane for this credential.
func CoolIngest(ctx context.Context, base, key string, wait time.Duration) {
	if os.Getenv("OPS_INGEST_BUDGET_ENABLED") != "true" {
		return
	}
	for _, lane := range []IngestLane{IngestUsage, IngestFunding, IngestMetadata} {
		coolWindow(ctx, ingestKey(base, key, lane), max(wait, time.Minute))
	}
}
