package main

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCutoverRejectsIncompleteApprovalBeforeOpeningDatabase(t *testing.T) {
	for _, args := range [][]string{
		nil,
		{"review-held", "--state", "held.json", "--scope", "overseas", "--evidence", "approval"},
		{"review-held", "--state", "held.json", "--scope", "overseas", "--evidence", "approval", "--bill-ids", "5,6"},
		{"prepare", "--state", "opening.json", "--scope", "overseas", "--evidence", "drain"},
		{"apply", "--state", "opening.json", "--scope", "overseas", "--evidence", "drain", "--writers-stopped"},
	} {
		var output bytes.Buffer
		require.Error(t, run(t.Context(), args, &output))
		require.Empty(t, output.String())
	}
	var output bytes.Buffer
	require.NoError(t, run(t.Context(), []string{"--help"}, &output))
	require.Contains(t, output.String(), "review-held")
}
