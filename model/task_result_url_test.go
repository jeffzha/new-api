package model

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Some OpenAI-compatible gateways (for example Uzoom) return their task record
// wrapped as {"code":"success","data":{... "result_url": "..."}}. When that
// envelope is decoded straight into model.Task, the artifact URL lands on the
// top-level ResultURL field instead of PrivateData.ResultURL. GetResultURL must
// surface it, otherwise the polling pipeline falls back to its own proxy URL and
// the public download endpoint returns 410 artifact_gone.
func TestTaskGetResultURLPrefersPrivateDataThenTopLevel(t *testing.T) {
	t.Run("private data wins", func(t *testing.T) {
		task := &Task{
			ResultURL:   "https://upstream.example/top-level.mp4",
			PrivateData: TaskPrivateData{ResultURL: "https://upstream.example/private.mp4"},
		}
		assert.Equal(t, "https://upstream.example/private.mp4", task.GetResultURL())
	})

	t.Run("top level fallback", func(t *testing.T) {
		task := &Task{ResultURL: "https://upstream.example/top-level.mp4"}
		assert.Equal(t, "https://upstream.example/top-level.mp4", task.GetResultURL())
	})

	t.Run("fail reason legacy fallback", func(t *testing.T) {
		task := &Task{FailReason: "https://upstream.example/legacy.mp4"}
		assert.Equal(t, "https://upstream.example/legacy.mp4", task.GetResultURL())
	})
}
