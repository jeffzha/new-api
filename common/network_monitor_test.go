package common

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestNetworkIOSampleRatesSince(t *testing.T) {
	startedAt := time.Unix(1_700_000_000, 0)
	tests := []struct {
		name                 string
		previous             networkIOSample
		current              networkIOSample
		expectedReceiveRate  float64
		expectedTransmitRate float64
	}{
		{
			name: "calculates average rates over the sample window",
			previous: networkIOSample{
				receivedBytes: 1_000,
				sentBytes:     2_000,
				sampledAt:     startedAt,
			},
			current: networkIOSample{
				receivedBytes: 4_000,
				sentBytes:     3_500,
				sampledAt:     startedAt.Add(30 * time.Second),
			},
			expectedReceiveRate:  100,
			expectedTransmitRate: 50,
		},
		{
			name: "does not report a negative spike after counters reset",
			previous: networkIOSample{
				receivedBytes: 4_000,
				sentBytes:     3_500,
				sampledAt:     startedAt,
			},
			current: networkIOSample{
				receivedBytes: 100,
				sentBytes:     200,
				sampledAt:     startedAt.Add(30 * time.Second),
			},
			expectedReceiveRate:  0,
			expectedTransmitRate: 0,
		},
		{
			name: "does not calculate rates for the first sample",
			current: networkIOSample{
				receivedBytes: 1_000,
				sentBytes:     2_000,
				sampledAt:     startedAt,
			},
			expectedReceiveRate:  0,
			expectedTransmitRate: 0,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			receiveRate, transmitRate := test.current.ratesSince(test.previous)
			assert.Equal(t, test.expectedReceiveRate, receiveRate)
			assert.Equal(t, test.expectedTransmitRate, transmitRate)
		})
	}
}
