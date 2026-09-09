package mobilecloudseedance

import (
	"crypto/sha256"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

const sdkClientTTL = time.Hour

type sdkClient interface {
	CreateVideoGenerationTask(data map[string]interface{}) (string, error)
	QueryVideoGenerationTask(taskID string) (map[string]interface{}, error)
}

type synchronizedSDKClient struct {
	mu     sync.Mutex
	client sdkClient
}

func (c *synchronizedSDKClient) CreateVideoGenerationTask(data map[string]interface{}) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.client.CreateVideoGenerationTask(data)
}

func (c *synchronizedSDKClient) QueryVideoGenerationTask(taskID string) (map[string]interface{}, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.client.QueryVideoGenerationTask(taskID)
}

type sdkClientFactory func(baseURL, apiKey, model string) (sdkClient, error)

type sdkClientProvider interface {
	Get(baseURL, apiKey, model string) (sdkClient, error)
}

type sdkClientEntry struct {
	client    sdkClient
	expiresAt time.Time
}

type sdkClientManager struct {
	clients sync.Map
	group   singleflight.Group
	now     func() time.Time
	factory sdkClientFactory
}

func newSDKClientManager(factory sdkClientFactory) *sdkClientManager {
	return &sdkClientManager{
		now:     time.Now,
		factory: factory,
	}
}

func (m *sdkClientManager) Get(baseURL, apiKey, model string) (sdkClient, error) {
	if m == nil || m.factory == nil {
		return nil, fmt.Errorf("Mobile Cloud Seedance SDK client factory is unavailable")
	}
	fingerprint := sdkClientFingerprint(baseURL, apiKey, model)
	if cached, ok := m.clients.Load(fingerprint); ok {
		entry := cached.(sdkClientEntry)
		if m.now().Before(entry.expiresAt) {
			return entry.client, nil
		}
		m.clients.Delete(fingerprint)
	}

	value, err, _ := m.group.Do(fingerprint, func() (interface{}, error) {
		if cached, ok := m.clients.Load(fingerprint); ok {
			entry := cached.(sdkClientEntry)
			if m.now().Before(entry.expiresAt) {
				return entry.client, nil
			}
			m.clients.Delete(fingerprint)
		}
		client, err := m.factory(baseURL, apiKey, model)
		if err != nil {
			return nil, err
		}
		synchronized := &synchronizedSDKClient{client: client}
		m.clients.Store(fingerprint, sdkClientEntry{
			client:    synchronized,
			expiresAt: m.now().Add(sdkClientTTL),
		})
		return synchronized, nil
	})
	if err != nil {
		return nil, err
	}
	return value.(sdkClient), nil
}

func sdkClientFingerprint(baseURL, apiKey, model string) string {
	sum := sha256.Sum256([]byte(baseURL + "\x00" + apiKey + "\x00" + model))
	return fmt.Sprintf("%x", sum[:])
}

var defaultSDKClients = newSDKClientManager(newOfficialSDKClient)
