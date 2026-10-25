package agencyopspush

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/QuantumNous/new-api/model"
)

// The Hub and its short-lived operator bridge share a container, not memory.
// A private datagram only wakes the existing durable sender; it carries no data.
func listenForCommits(ctx context.Context, path string) error {
	if path == "" {
		return nil
	}
	if !filepath.IsAbs(path) || len(path) > 100 {
		return errors.New("invalid distributor wake socket")
	}
	directory := filepath.Dir(path)
	if err := os.Mkdir(directory, 0700); err != nil && !os.IsExist(err) {
		return errors.New("distributor wake directory unavailable")
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return errors.New("distributor wake directory is not private")
	}
	if info, err := os.Lstat(path); err == nil {
		if info.Mode()&os.ModeSocket == 0 {
			return errors.New("distributor wake path is not a socket")
		}
		// Only a refused connection proves a stale socket. A full queue or
		// permission error must never replace another running listener.
		probe, err := net.DialTimeout("unixgram", path, 100*time.Millisecond)
		if err == nil {
			probe.Close()
			return errors.New("distributor wake listener already active")
		}
		if !errors.Is(err, syscall.ECONNREFUSED) {
			return errors.New("distributor wake listener state unknown")
		}
		if err := os.Remove(path); err != nil {
			return errors.New("distributor stale wake socket unavailable")
		}
	} else if !os.IsNotExist(err) {
		return errors.New("distributor wake path unavailable")
	}
	connection, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		return errors.New("distributor wake listener unavailable")
	}
	if err := os.Chmod(path, 0600); err != nil {
		connection.Close()
		return errors.New("distributor wake socket permissions unavailable")
	}
	go func() {
		<-ctx.Done()
		connection.Close()
	}()
	go func() {
		defer os.Remove(path)
		var message [2]byte
		for {
			size, _, err := connection.ReadFromUnix(message[:])
			if err != nil {
				return
			}
			if size == 1 && message[0] == 1 {
				select {
				case model.AgencyOpsCommitted <- struct{}{}:
				default:
				}
			}
		}
	}()
	return nil
}

func notifyCommit(path string) bool {
	if !filepath.IsAbs(path) || len(path) > 100 {
		return false
	}
	connection, err := net.DialTimeout("unixgram", path, 100*time.Millisecond)
	if err != nil {
		return false
	}
	defer connection.Close()
	if err := connection.SetWriteDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		return false
	}
	_, err = connection.Write([]byte{1})
	return err == nil
}

// NotifyConfiguredCommit is best effort after a successful bridge transaction.
// A lost hint must not turn a committed command into a retry or block its reply.
func NotifyConfiguredCommit() {
	if os.Getenv("OPS_DISTRIBUTOR_EVENTS_ENABLED") == "true" {
		notifyCommit(os.Getenv("OPS_DISTRIBUTOR_WAKE_SOCKET"))
	}
}
