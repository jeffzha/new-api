package agencyhub

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestTrustedBrowserOriginCanDifferFromInvitationOrigin(t *testing.T) {
	app := &App{config: Config{
		PublicBaseURL: "http://localhost:3001",
		BrowserOrigin: "http://localhost:3202",
	}}

	assert.Equal(t, "http://localhost:3202", app.trustedBrowserOrigin())
	assert.True(t, sameConfiguredOrigin("http://localhost:3202", app.trustedBrowserOrigin()))
	assert.False(t, sameConfiguredOrigin("http://localhost:3001", app.trustedBrowserOrigin()))
}

func TestTrustedBrowserOriginDefaultsToPublicBaseURL(t *testing.T) {
	app := &App{config: Config{PublicBaseURL: "https://gateway.example"}}

	assert.Equal(t, "https://gateway.example", app.trustedBrowserOrigin())
}
