package billingexpr

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTaskUsageExpressionUsesFactsAndTaskQuotaConversion(t *testing.T) {
	expression := `tier("1080p", u("seconds") * (u("resolution") == "1080p" ? 0.4 : 0.2))`
	cost, trace, err := RunExprWithRequest(expression, TokenParams{}, RequestInput{Usage: map[string]any{"seconds": 10.0, "resolution": "1080p"}})
	require.NoError(t, err)
	assert.Equal(t, 4.0, cost)
	assert.Equal(t, "1080p", trace.MatchedTier)
	result, err := ComputeTieredQuotaWithRequest(&BillingSnapshot{ExprString: expression, ExprHash: ExprHashString(expression), GroupRatio: 2, QuotaPerUnit: 500000, ExprVersion: 1, TaskUsageBilling: true}, TokenParams{}, RequestInput{Usage: map[string]any{"seconds": 10.0, "resolution": "1080p"}})
	require.NoError(t, err)
	assert.Equal(t, 4_000_000, result.ActualQuotaAfterGroup)
}

func TestUsageFactBillingUsesUnitQuotaConversion(t *testing.T) {
	expression := `tier("base", u("seconds") * 0.00003013698630136986)`
	snapshot := &BillingSnapshot{
		ExprString: expression, ExprHash: ExprHashString(expression), GroupRatio: 1,
		QuotaPerUnit: 500_000, ExprVersion: 1, UsageFactsBilling: true,
	}
	result, err := ComputeTieredQuotaWithRequest(snapshot, TokenParams{}, RequestInput{Usage: map[string]any{"seconds": 10.0}})
	require.NoError(t, err)
	assert.InDelta(t, 150.6849315068493, result.ActualQuotaBeforeGroup, 1e-9)
	assert.Equal(t, 151, result.ActualQuotaAfterGroup)
}
