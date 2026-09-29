package common

import (
	"sync"
	"sync/atomic"
	"testing"
)

func TestConsumeCodeWithKeyIsSingleUseUnderConcurrency(t *testing.T) {
	const key = "atomic-verification@example.com"
	const code = "826431"
	RegisterVerificationCodeWithKey(key, code, EmailVerificationPurpose)
	t.Cleanup(func() { DeleteKey(key, EmailVerificationPurpose) })

	var successes atomic.Int32
	var wait sync.WaitGroup
	for range 16 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			if ConsumeCodeWithKey(key, code, EmailVerificationPurpose) {
				successes.Add(1)
			}
		}()
	}
	wait.Wait()
	if got := successes.Load(); got != 1 {
		t.Fatalf("verification code was consumed %d times, want 1", got)
	}
}
