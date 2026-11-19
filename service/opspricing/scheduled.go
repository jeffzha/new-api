package opspricing

import (
	"errors"
	"time"
)

type scheduledPrice struct {
	EffectiveAt string         `json:"effectiveAt"`
	Official    map[string]any `json:"officialCny"`
}

func validateSchedule(prices []scheduledPrice) error {
	if len(prices) > 20 {
		return errors.New("OPS scheduled price limit exceeded")
	}
	var previous time.Time
	for _, p := range prices {
		at, err := time.Parse(time.RFC3339Nano, p.EffectiveAt)
		if err != nil || !at.After(previous) {
			return errors.New("invalid OPS scheduled price")
		}
		previous = at
	}
	return nil
}

// Evaluate the retained schedule against the clock on each display read. No
// polling loop or database write is needed, including while OPS is unavailable.
func (p priceEntry) officialAt(at time.Time) map[string]any {
	current := p.Official
	for _, next := range p.Upcoming {
		due, err := time.Parse(time.RFC3339Nano, next.EffectiveAt)
		if err != nil || at.Before(due) {
			break
		}
		current = next.Official
	}
	return current
}
