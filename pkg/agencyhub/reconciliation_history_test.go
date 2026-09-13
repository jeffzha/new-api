package agencyhub

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestReconciliationHistoryPaginatesExactIDsAndRejectsForeignCursor(t *testing.T) {
	client := newFinanceRootClient(t)
	finished := int64(1789228805000)
	runs := []model.AgencyReconciliationRun{
		{ID: 9007199254740993, RunKey: "history-one", Trigger: "manual", Status: "completed", StartedAtMS: 1789228800000, FinishedAtMS: &finished},
		{ID: 9007199254740994, RunKey: "history-two", Trigger: "scheduled", Status: "failed", StartedAtMS: 1789228801000, Error: "database unavailable"},
	}
	require.NoError(t, client.app.db.Create(&runs).Error)
	get := func(path string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.AddCookie(&http.Cookie{Name: client.app.config.CookieName, Value: client.sessionToken})
		response := httptest.NewRecorder()
		client.app.Router().ServeHTTP(response, request)
		return response
	}
	var first struct {
		Data struct {
			Items []struct {
				ID        string `json:"id"`
				StartedAt string `json:"started_at_ms"`
				Error     string `json:"error"`
			} `json:"items"`
			Total int `json:"total"`
			Meta  struct {
				Next string `json:"next_cursor"`
			} `json:"meta"`
		} `json:"data"`
	}
	response := get("/agency/api/v1/root/reconciliation/runs?page_size=1")
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	require.NoError(t, common.Unmarshal(response.Body.Bytes(), &first))
	require.Len(t, first.Data.Items, 1)
	assert.Equal(t, "9007199254740994", first.Data.Items[0].ID)
	assert.Equal(t, "1789228801000", first.Data.Items[0].StartedAt)
	assert.Equal(t, "database unavailable", first.Data.Items[0].Error)
	assert.Equal(t, 2, first.Data.Total)
	require.NotEmpty(t, first.Data.Meta.Next)
	cursor := url.QueryEscape(first.Data.Meta.Next)
	second := get("/agency/api/v1/root/reconciliation/runs?page_size=1&cursor=" + cursor)
	require.Equal(t, http.StatusOK, second.Code, second.Body.String())
	assert.Contains(t, second.Body.String(), `"id":"9007199254740993"`)
	assert.Contains(t, second.Body.String(), `"finished_at_ms":"1789228805000"`)
	assert.Contains(t, second.Body.String(), `"next_cursor":""`)
	assert.NotContains(t, second.Body.String(), "history-two")
	for _, path := range []string{
		"/agency/api/v1/root/reconciliation/issues?page_size=1&cursor=" + cursor,
		"/agency/api/v1/root/reconciliation/runs?page=2&cursor=" + cursor,
		"/agency/api/v1/root/reconciliation/runs?page_size=1&cursor=" + cursor + "broken",
	} {
		rejected := get(path)
		assert.Equal(t, http.StatusBadRequest, rejected.Code, rejected.Body.String())
	}
}
