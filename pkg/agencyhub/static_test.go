package agencyhub

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/net/html"
)

func TestIndexUsesRegionalQuotaDisplaySettings(t *testing.T) {
	settings := operation_setting.GetGeneralSetting()
	previousCurrency, previousRate, previousQuota := settings.QuotaDisplayType, operation_setting.USDExchangeRate, common.QuotaPerUnit
	t.Cleanup(func() {
		settings.QuotaDisplayType, operation_setting.USDExchangeRate, common.QuotaPerUnit = previousCurrency, previousRate, previousQuota
	})
	for _, tc := range []struct {
		currency, symbol string
		rate             float64
	}{
		{"USD", "$", 1},
		{"CNY", "¥", 7.1},
	} {
		t.Run(tc.currency, func(t *testing.T) {
			settings.QuotaDisplayType = tc.currency
			operation_setting.USDExchangeRate = 7.1
			common.QuotaPerUnit = 1000000
			app := New(nil, nil, Config{BasePath: "/agency"})
			recorder := httptest.NewRecorder()
			app.Router().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/agency/", nil))
			require.Equal(t, http.StatusOK, recorder.Code)
			_, after, found := strings.Cut(recorder.Body.String(), "window.__AGENCY_CONFIG__=")
			require.True(t, found)
			configJSON, _, found := strings.Cut(after, "</script>")
			require.True(t, found)
			var config struct {
				QuotaDisplay struct {
					CurrencyCode string  `json:"currency_code"`
					Symbol       string  `json:"currency_symbol"`
					QuotaPerUnit float64 `json:"quota_per_unit"`
					Rate         float64 `json:"usd_to_currency_rate"`
				} `json:"quota_display"`
			}
			require.NoError(t, common.UnmarshalJsonStr(configJSON, &config))
			assert.Equal(t, tc.currency, config.QuotaDisplay.CurrencyCode)
			assert.Equal(t, tc.symbol, config.QuotaDisplay.Symbol)
			assert.Equal(t, float64(1000000), config.QuotaDisplay.QuotaPerUnit)
			assert.Equal(t, tc.rate, config.QuotaDisplay.Rate)
		})
	}
}

func TestIndexEmbedsPlatformBaseURLAndSSOWiring(t *testing.T) {
	app := newAgencyTestApp(t)
	app.config.PlatformBaseURL = "http://127.0.0.1:3000"
	recorder := httptest.NewRecorder()
	app.Router().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/agency", nil))
	require.Equal(t, http.StatusOK, recorder.Code)
	body := recorder.Body.String()
	require.Contains(t, body, `window.__AGENCY_CONFIG__=`)
	require.Contains(t, body, `"platform_base_url":"http://127.0.0.1:3000"`)
	require.Contains(t, body, "/agency/assets/")

	app2 := newAgencyTestApp(t)
	app2.config.PlatformBaseURL = ""
	recorder2 := httptest.NewRecorder()
	app2.Router().ServeHTTP(recorder2, httptest.NewRequest(http.MethodGet, "/agency/", nil))
	require.Equal(t, http.StatusOK, recorder2.Code)
	require.Contains(t, recorder2.Body.String(), `window.__AGENCY_CONFIG__=`)
}

func TestAgencyStaticResourcesFollowIndex(t *testing.T) {
	for _, basePath := range []string{"/agency", "/partners/agency"} {
		t.Run(basePath, func(t *testing.T) {
			app := New(nil, nil, Config{BasePath: basePath})
			router := app.Router()
			for _, indexPath := range []string{basePath, basePath + "/"} {
				t.Run(indexPath, func(t *testing.T) {
					page := httptest.NewRecorder()
					router.ServeHTTP(page, httptest.NewRequest(http.MethodGet, indexPath, nil))
					require.Equal(t, http.StatusOK, page.Code)
					assert.Equal(t, "text/html; charset=utf-8", page.Header().Get("Content-Type"))
					assert.Equal(t, "no-store", page.Header().Get("Cache-Control"))
					assert.Contains(t, page.Body.String(), `"base_path":"`+basePath+`"`)

					// Fetch the actual module and stylesheet referenced by the served
					// HTML, so an embedded index with unreachable assets cannot pass.
					tokenizer := html.NewTokenizer(strings.NewReader(page.Body.String()))
					moduleCount, stylesheetCount := 0, 0
					for {
						tokenType := tokenizer.Next()
						if tokenType == html.ErrorToken {
							require.ErrorIs(t, tokenizer.Err(), io.EOF)
							break
						}
						if tokenType != html.StartTagToken && tokenType != html.SelfClosingTagToken {
							continue
						}
						token := tokenizer.Token()
						attrs := make(map[string]string, len(token.Attr))
						for _, attr := range token.Attr {
							attrs[attr.Key] = attr.Val
						}
						var resourceURL, contentType string
						switch {
						case token.Data == "script" && attrs["src"] != "":
							assert.Equal(t, "module", attrs["type"])
							resourceURL, contentType = attrs["src"], "text/javascript; charset=utf-8"
							moduleCount++
						case token.Data == "link" && attrs["rel"] == "stylesheet":
							resourceURL, contentType = attrs["href"], "text/css; charset=utf-8"
							stylesheetCount++
						default:
							continue
						}
						require.True(t, strings.HasPrefix(resourceURL, basePath+"/assets/"), "resource escaped configured base path: %s", resourceURL)
						asset := httptest.NewRecorder()
						router.ServeHTTP(asset, httptest.NewRequest(http.MethodGet, resourceURL, nil))
						require.Equal(t, http.StatusOK, asset.Code, resourceURL)
						assert.Equal(t, contentType, asset.Header().Get("Content-Type"), resourceURL)
						assert.Equal(t, "nosniff", asset.Header().Get("X-Content-Type-Options"))
						assert.Equal(t, "public, max-age=31536000, immutable", asset.Header().Get("Cache-Control"))
						require.NotEmpty(t, asset.Body.Bytes(), resourceURL)
						body := strings.ToLower(strings.TrimSpace(asset.Body.String()))
						assert.False(t, strings.HasPrefix(body, "<!doctype html"), "asset fell back to index: %s", resourceURL)
						assert.False(t, strings.HasPrefix(body, "<html"), "asset fell back to index: %s", resourceURL)
					}
					assert.Positive(t, moduleCount, "index must reference a JavaScript module")
					assert.Positive(t, stylesheetCount, "index must reference a stylesheet")
				})
			}

			for _, resourcePath := range []string{
				basePath + "/assets/missing-module.js",
				basePath + "/assets/missing-style.css",
				basePath + "/assets/../index.html",
				basePath + "/assets/%2e%2e/index.html",
				basePath + "/assets/",
			} {
				t.Run(resourcePath, func(t *testing.T) {
					response := httptest.NewRecorder()
					router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, resourcePath, nil))
					assert.Equal(t, http.StatusNotFound, response.Code)
					assert.NotContains(t, response.Header().Get("Content-Type"), "text/html")
					assert.NotContains(t, response.Body.String(), "<!doctype html")
					assert.NotContains(t, response.Body.String(), "__AGENCY_CONFIG__")
				})
			}
		})
	}
}
