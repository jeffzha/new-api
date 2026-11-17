package middleware

import (
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type blockedAccessLog struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (w *blockedAccessLog) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.entered) })
	<-w.release
	return len(p), nil
}

func TestHealthProbesDoNotWaitForAccessLogsOrBusinessLocks(t *testing.T) {
	previousWriter := gin.DefaultWriter
	writer := &blockedAccessLog{entered: make(chan struct{}), release: make(chan struct{})}
	gin.DefaultWriter = writer
	t.Cleanup(func() { gin.DefaultWriter = previousWriter })
	previousMode := gin.Mode()
	gin.SetMode(gin.TestMode)
	t.Cleanup(func() { gin.SetMode(previousMode) })

	server := gin.New()
	RegisterHealthCheck(server)
	SetUpLogger(server)
	server.GET("/api/status", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"success": true})
	})
	server.GET("/business", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})
	httpServer := httptest.NewServer(server)
	defer httpServer.Close()
	defer close(writer.release)
	client := &http.Client{Timeout: 2 * time.Second}
	businessDone := make(chan error, 1)
	go func() {
		response, err := client.Get(httpServer.URL + "/business")
		if err == nil {
			_, err = io.Copy(io.Discard, response.Body)
			response.Body.Close()
		}
		businessDone <- err
	}()
	select {
	case <-writer.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("business request did not enter the blocked log writer")
	}

	common.OptionMapRWMutex.Lock()
	defer common.OptionMapRWMutex.Unlock()
	for _, path := range []string{"/api/status?probe=uncached", "/healthz"} {
		response, err := client.Get(httpServer.URL + path)
		require.NoError(t, err, path)
		body, err := io.ReadAll(response.Body)
		response.Body.Close()
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, response.StatusCode)
		if path == "/healthz" {
			assert.JSONEq(t, `{"status":"ready","version":"`+common.Version+`"}`, string(body))
			assert.Equal(t, "no-store", response.Header.Get("Cache-Control"))
		} else {
			assert.JSONEq(t, `{"success":true}`, string(body))
		}
	}
	select {
	case err := <-businessDone:
		t.Fatalf("the ordinary request unexpectedly bypassed its blocked log: %v", err)
	default:
	}
}
