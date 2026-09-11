package openai

import (
	"testing"

	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/stretchr/testify/require"
)

func TestRealtimeUsageDeltaClampsNonMonotonicFrames(t *testing.T) {
	previous := &dto.RealtimeUsage{
		TotalTokens: 100, InputTokens: 60, OutputTokens: 40,
		InputTokenDetails:  dto.InputTokenDetails{TextTokens: 50, CachedTokens: 10},
		OutputTokenDetails: dto.OutputTokenDetails{TextTokens: 40, ReasoningTokens: 3},
	}
	current := &dto.RealtimeUsage{
		TotalTokens: 120, InputTokens: 70, OutputTokens: 50,
		InputTokenDetails:  dto.InputTokenDetails{TextTokens: 45, CachedTokens: 12},
		OutputTokenDetails: dto.OutputTokenDetails{TextTokens: 48, ReasoningTokens: 1},
	}
	delta := realtimeUsageDelta(current, previous)
	require.Equal(t, 20, delta.TotalTokens)
	require.Equal(t, 10, delta.InputTokens)
	require.Equal(t, 10, delta.OutputTokens)
	require.Equal(t, 0, delta.InputTokenDetails.TextTokens)
	require.Equal(t, 2, delta.InputTokenDetails.CachedTokens)
	require.Equal(t, 8, delta.OutputTokenDetails.TextTokens)
	require.Equal(t, 0, delta.OutputTokenDetails.ReasoningTokens)
}

func TestRealtimeUsageMonotonicRejectsCounterRollback(t *testing.T) {
	previous := &dto.RealtimeUsage{TotalTokens: 100, InputTokens: 60}
	current := &dto.RealtimeUsage{TotalTokens: 99, InputTokens: 70}
	require.False(t, realtimeUsageMonotonic(current, previous))
	current.TotalTokens = 100
	require.True(t, realtimeUsageMonotonic(current, previous))
}
