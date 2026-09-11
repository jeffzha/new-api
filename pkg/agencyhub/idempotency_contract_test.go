package agencyhub

import (
	"bytes"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestNormalizeIdempotencyBodyUsesCanonicalJSON(t *testing.T) {
	first := normalizeIdempotencyBody([]byte(`{"a":1, "nested":{"b":true}, "text":"x"}`))
	second := normalizeIdempotencyBody([]byte(` { "text":"x","nested":{"b":true},"a":01} `))
	require.NotEqual(t, first, second, "non-canonical JSON numbers must not alias a valid request")

	second = normalizeIdempotencyBody([]byte(` { "text":"x","nested":{"b":true},"a":1} `))
	require.True(t, bytes.Equal(first, second), "equivalent JSON objects must share an idempotency body hash")
}

func TestNormalizeIdempotencyBodyKeepsMalformedBodiesDistinct(t *testing.T) {
	first := normalizeIdempotencyBody([]byte(`{"a":`))
	second := normalizeIdempotencyBody([]byte(`{"a": }`))
	require.NotEqual(t, first, second)
}

func TestAgencyPreviewPathsDoNotRequireIdempotencyKey(t *testing.T) {
	require.True(t, isAgencyPreviewPath("/agency/api/v1/pricing/sales/preview"))
	require.True(t, isAgencyPreviewPath("/agency/api/v1/root/agencies/7/pricing/preview"))
	require.False(t, isAgencyPreviewPath("/agency/api/v1/pricing/sales/publish"))
}

func TestAgencyCursorIsSignedAndBoundToActorAndFilters(t *testing.T) {
	app := newAgencyTestApp(t)
	app.config.CursorSecret = "test cursor secret that is at least thirty-two bytes"
	app.cursorSecret = []byte(app.config.CursorSecret)
	agencyID := int64(7)
	identity := &Identity{ActorType: ActorTypeOperator, ActorID: 11, AgencyID: &agencyID}
	context, _ := gin.CreateTestContext(httptest.NewRecorder())
	context.Request = httptest.NewRequest("GET", "/agency/api/v1/customers?page_size=2&status=active", nil)
	cursor, err := app.encodeCursor(agencyCursor{
		Kind:      "customers",
		Scope:     cursorScope(context, "customers", identity),
		ActorType: identity.ActorType,
		ActorID:   identity.ActorID,
		AgencyID:  agencyID,
		PositionU: 12,
	})
	require.NoError(t, err)
	decoded, err := app.decodeCursor(cursor, context, "customers", identity)
	require.NoError(t, err)
	require.Equal(t, int64(12), decoded.PositionU)

	context.Request = httptest.NewRequest("GET", "/agency/api/v1/customers?page_size=2&status=disabled", nil)
	_, err = app.decodeCursor(cursor, context, "customers", identity)
	require.Error(t, err)
	identity.ActorID++
	_, err = app.decodeCursor(cursor, context, "customers", identity)
	require.Error(t, err)
}

func TestDecimalInt64RequiresCanonicalString(t *testing.T) {
	var value decimalInt64
	require.NoError(t, value.UnmarshalJSON([]byte(`"9223372036854775807"`)))
	require.Equal(t, int64(9223372036854775807), value.Int64())
	encoded, err := value.MarshalJSON()
	require.NoError(t, err)
	require.Equal(t, `"9223372036854775807"`, string(encoded))
	require.Error(t, value.UnmarshalJSON([]byte(`9223372036854775807`)))
	require.Error(t, value.UnmarshalJSON([]byte(`"01"`)))
}
