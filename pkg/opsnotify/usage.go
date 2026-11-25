// Package opsnotify provides a bounded hint for committed usage logs.
// The SQL log and persistent push cursor remain the delivery authority.
package opsnotify

var committed = make(chan int, 1024)
var funding = make(chan struct{}, 1)

// UsageCommitted never waits for the sender, network or database. Losing a hint
// is safe: the existing durable scanner still sends the committed log.
func UsageCommitted(id int) {
	if id <= 0 {
		return
	}
	select {
	case committed <- id:
	default:
	}
}

func Committed() <-chan int { return committed }

// FundingCommitted coalesces post-commit hints. The outbox is the authority;
// a lost hint or a process restart is recovered by the funding scanner.
func FundingCommitted() {
	select {
	case funding <- struct{}{}:
	default:
	}
}

func Funding() <-chan struct{} { return funding }
