package common

import (
	"fmt"
	"sync"
	"time"

	gopsutilnet "github.com/shirou/gopsutil/net"
)

// NetworkIOStatus contains cumulative counters and average transfer rates.
type NetworkIOStatus struct {
	ReceivedBytes          uint64
	SentBytes              uint64
	ReceiveBytesPerSecond  float64
	TransmitBytesPerSecond float64
}

type networkIOSample struct {
	receivedBytes uint64
	sentBytes     uint64
	sampledAt     time.Time
}

var networkIOSampler struct {
	sync.Mutex
	previous networkIOSample
}

func (current networkIOSample) ratesSince(previous networkIOSample) (float64, float64) {
	elapsedSeconds := current.sampledAt.Sub(previous.sampledAt).Seconds()
	if previous.sampledAt.IsZero() || elapsedSeconds <= 0 {
		return 0, 0
	}

	var receiveRate float64
	if current.receivedBytes >= previous.receivedBytes {
		receiveRate = float64(current.receivedBytes-previous.receivedBytes) / elapsedSeconds
	}

	var transmitRate float64
	if current.sentBytes >= previous.sentBytes {
		transmitRate = float64(current.sentBytes-previous.sentBytes) / elapsedSeconds
	}

	return receiveRate, transmitRate
}

// GetNetworkIOStatus samples aggregate network I/O for the current network namespace.
func GetNetworkIOStatus() (NetworkIOStatus, error) {
	networkIOSampler.Lock()
	defer networkIOSampler.Unlock()

	counters, err := gopsutilnet.IOCounters(false)
	if err != nil {
		return NetworkIOStatus{}, fmt.Errorf("read network I/O counters: %w", err)
	}
	if len(counters) == 0 {
		return NetworkIOStatus{}, fmt.Errorf("read network I/O counters: no interfaces found")
	}

	current := networkIOSample{
		receivedBytes: counters[0].BytesRecv,
		sentBytes:     counters[0].BytesSent,
		sampledAt:     time.Now(),
	}

	receiveRate, transmitRate := current.ratesSince(networkIOSampler.previous)
	networkIOSampler.previous = current

	return NetworkIOStatus{
		ReceivedBytes:          current.receivedBytes,
		SentBytes:              current.sentBytes,
		ReceiveBytesPerSecond:  receiveRate,
		TransmitBytesPerSecond: transmitRate,
	}, nil
}
