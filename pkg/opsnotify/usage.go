// Package opsnotify carries bounded post-commit hints, never financial data.
package opsnotify

var committed = make(chan string, 1)

// Committed may only be called after the outer settlement transaction commits.
// Lost hints are harmless: the durable outbox scanner remains authoritative.
func Committed(scope string) {
	if scope == "" {
		return
	}
	select {
	case committed <- scope:
	default:
	}
}

func Wake() <-chan string { return committed }
