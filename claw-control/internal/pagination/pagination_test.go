package pagination_test

import (
	"testing"

	"github.com/QuantumNous/new-api/claw-control/internal/pagination"
	"github.com/stretchr/testify/assert"
)

func TestAdministrativePageLimitContract(t *testing.T) {
	for _, testCase := range []struct {
		requested int
		expected  int
	}{
		{-1, 100}, {0, 100}, {1, 1}, {200, 200}, {201, 200},
	} {
		assert.Equal(t, testCase.expected, pagination.Limit(testCase.requested))
	}
}
