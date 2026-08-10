package evidence_test

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/claw-control/internal/evidence"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestClamAVScannerAcceptsOnlyExplicitCleanResponse(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		response string
		wantErr  string
	}{
		{name: "clean", response: "stream: OK\x00"},
		{name: "detected", response: "stream: Eicar-Test-Signature FOUND\x00", wantErr: "rejected by malware scanning"},
		{name: "unknown", response: "stream: UNKNOWN\x00", wantErr: "trusted clean result"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			require.NoError(t, err)
			defer listener.Close()
			serverError := make(chan error, 1)
			go func() {
				connection, acceptErr := listener.Accept()
				if acceptErr != nil {
					serverError <- acceptErr
					return
				}
				defer connection.Close()
				command := make([]byte, len("zINSTREAM\x00"))
				if _, readErr := io.ReadFull(connection, command); readErr != nil {
					serverError <- readErr
					return
				}
				for {
					var length uint32
					if readErr := binary.Read(connection, binary.BigEndian, &length); readErr != nil {
						serverError <- readErr
						return
					}
					if length == 0 {
						break
					}
					if _, readErr := io.CopyN(io.Discard, connection, int64(length)); readErr != nil {
						serverError <- readErr
						return
					}
				}
				_, writeErr := connection.Write([]byte(testCase.response))
				serverError <- writeErr
			}()
			scanner, err := evidence.NewClamAVScanner(listener.Addr().String(), time.Second)
			require.NoError(t, err)
			err = scanner.Scan(context.Background(), []byte("bounded evidence"))
			if testCase.wantErr == "" {
				assert.NoError(t, err)
			} else {
				assert.ErrorContains(t, err, testCase.wantErr)
			}
			require.NoError(t, <-serverError)
		})
	}
}

func TestClamAVScannerFailsClosedWhenUnavailable(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	address := listener.Addr().String()
	require.NoError(t, listener.Close())
	scanner, err := evidence.NewClamAVScanner(address, 100*time.Millisecond)
	require.NoError(t, err)
	assert.ErrorContains(t, scanner.Scan(context.Background(), []byte("evidence")), "unavailable")
}
