package dto

import (
	"testing"

	kitutil "github.com/QuantumNous/new-api/relaykit/relayconvert/kitutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestOpenAITopLevelCacheControlIsConversionOnly(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		decode  func([]byte) (any, error)
		marshal func(any) ([]byte, error)
	}{
		{
			name: "chat completions",
			raw:  `{"model":"m","cache_control":{"type":"ephemeral"},"tools":[{"type":"function","cache_control":{"type":"ephemeral"},"function":{"name":"f"}}]}`,
			decode: func(raw []byte) (any, error) {
				var request GeneralOpenAIRequest
				return &request, kitutil.Unmarshal(raw, &request)
			},
			marshal: func(value any) ([]byte, error) { return kitutil.Marshal(value.(*GeneralOpenAIRequest)) },
		},
		{
			name: "responses",
			raw:  `{"model":"m","cache_control":{"type":"ephemeral"}}`,
			decode: func(raw []byte) (any, error) {
				var request OpenAIResponsesRequest
				return &request, kitutil.Unmarshal(raw, &request)
			},
			marshal: func(value any) ([]byte, error) { return kitutil.Marshal(value.(*OpenAIResponsesRequest)) },
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request, err := test.decode([]byte(test.raw))
			require.NoError(t, err)
			wire, err := test.marshal(request)
			require.NoError(t, err)
			assert.NotContains(t, string(wire), "cache_control")
		})
	}
}
