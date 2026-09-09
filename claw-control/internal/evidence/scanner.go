package evidence

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/claw-control/internal/domain"
)

const clamAVChunkBytes = 64 << 10

type Scanner interface {
	Scan(context.Context, []byte) error
}

type ScannerFunc func(context.Context, []byte) error

func (function ScannerFunc) Scan(ctx context.Context, content []byte) error {
	return function(ctx, content)
}

type ClamAVScanner struct {
	address string
	timeout time.Duration
}

func NewClamAVScanner(address string, timeout time.Duration) (*ClamAVScanner, error) {
	address = strings.TrimSpace(address)
	if _, _, err := net.SplitHostPort(address); err != nil {
		return nil, errors.New("evidence ClamAV address must include a valid host and port")
	}
	if timeout <= 0 || timeout > time.Minute {
		return nil, errors.New("evidence ClamAV timeout must be between 1ns and 1m")
	}
	return &ClamAVScanner{address: address, timeout: timeout}, nil
}

func (s *ClamAVScanner) Scan(ctx context.Context, content []byte) error {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	connection, err := (&net.Dialer{}).DialContext(ctx, "tcp", s.address)
	if err != nil {
		return domain.Unavailable("evidence malware scanner is unavailable")
	}
	defer connection.Close()
	deadline, ok := ctx.Deadline()
	if ok {
		if err := connection.SetDeadline(deadline); err != nil {
			return domain.Unavailable("evidence malware scanner is unavailable")
		}
	}
	if err := writeClamAVBytes(connection, []byte("zINSTREAM\x00")); err != nil {
		return domain.Unavailable("evidence malware scanner is unavailable")
	}
	for offset := 0; offset < len(content); {
		end := offset + clamAVChunkBytes
		if end > len(content) {
			end = len(content)
		}
		var length [4]byte
		binary.BigEndian.PutUint32(length[:], uint32(end-offset))
		if err := writeClamAVBytes(connection, length[:]); err != nil {
			return domain.Unavailable("evidence malware scanner is unavailable")
		}
		if err := writeClamAVBytes(connection, content[offset:end]); err != nil {
			return domain.Unavailable("evidence malware scanner is unavailable")
		}
		offset = end
	}
	if err := writeClamAVBytes(connection, []byte{0, 0, 0, 0}); err != nil {
		return domain.Unavailable("evidence malware scanner is unavailable")
	}
	response, err := bufio.NewReader(io.LimitReader(connection, 4097)).ReadString(0)
	if err != nil {
		return domain.Unavailable("evidence malware scanner is unavailable")
	}
	if len(response) > 4096 {
		return domain.Unavailable("evidence malware scanner returned an invalid response")
	}
	response = strings.TrimSpace(strings.TrimRight(response, "\x00"))
	if response == "stream: OK" {
		return nil
	}
	if strings.HasSuffix(response, " FOUND") {
		return domain.Invalid("evidence was rejected by malware scanning")
	}
	return domain.Unavailable("evidence malware scan did not produce a trusted clean result")
}

func writeClamAVBytes(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		written, err := writer.Write(data)
		if err != nil {
			return err
		}
		if written <= 0 {
			return io.ErrUnexpectedEOF
		}
		data = data[written:]
	}
	return nil
}
