package test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/failsafe-go/failsafe-go"
	"github.com/failsafe-go/failsafe-go/internal/testutil"
	"github.com/failsafe-go/failsafe-go/retrypolicy"
)

// TestRetryPolicyDelayModeSwitching proves, through the public OnRetryScheduled listener and the execution result,
// that the delays observed at runtime come only from the last configured delay mode. Final modes use nanosecond
// scale delays so that no real waiting occurs.
func TestRetryPolicyDelayModeSwitching(t *testing.T) {
	nanoBackoff := []time.Duration{time.Nanosecond, 2 * time.Nanosecond, 4 * time.Nanosecond, 4 * time.Nanosecond}
	nanoDelayFunc := []time.Duration{time.Nanosecond, 2 * time.Nanosecond, 3 * time.Nanosecond, 4 * time.Nanosecond}
	delayFunc := func(exec failsafe.ExecutionAttempt[any]) time.Duration {
		return time.Duration(exec.Retries()+1) * time.Nanosecond
	}

	t.Run("backoff then random delay", func(t *testing.T) {
		delays, attempts, err := runAndRecordDelays(func(b retrypolicy.Builder[any]) retrypolicy.Builder[any] {
			return b.WithBackoff(50*time.Millisecond, 100*time.Millisecond).
				WithRandomDelay(time.Nanosecond, 10*time.Nanosecond)
		})
		assert.True(t, retrypolicy.IsExceededError(err))
		assert.Equal(t, 5, attempts)
		for i, d := range delays {
			assert.GreaterOrEqual(t, d, time.Nanosecond, "delay %d should not contain a stale backoff delay", i)
			assert.LessOrEqual(t, d, 10*time.Nanosecond, "delay %d should not contain a stale backoff delay", i)
		}
	})

	t.Run("random then fixed delay", func(t *testing.T) {
		delays, attempts, err := runAndRecordDelays(func(b retrypolicy.Builder[any]) retrypolicy.Builder[any] {
			return b.WithRandomDelay(time.Nanosecond, 10*time.Nanosecond).
				WithDelay(5 * time.Nanosecond)
		})
		assert.True(t, retrypolicy.IsExceededError(err))
		assert.Equal(t, 5, attempts)
		assert.Equal(t, []time.Duration{5, 5, 5, 5}, delays)
	})

	t.Run("fixed then backoff delay", func(t *testing.T) {
		delays, _, err := runAndRecordDelays(func(b retrypolicy.Builder[any]) retrypolicy.Builder[any] {
			return b.WithDelay(9*time.Nanosecond).
				WithBackoffFactor(time.Nanosecond, 4*time.Nanosecond, 2)
		})
		assert.True(t, retrypolicy.IsExceededError(err))
		assert.Equal(t, nanoBackoff, delays)
	})

	t.Run("backoff then fixed delay", func(t *testing.T) {
		delays, _, err := runAndRecordDelays(func(b retrypolicy.Builder[any]) retrypolicy.Builder[any] {
			return b.WithBackoffFactor(time.Nanosecond, 4*time.Nanosecond, 2).
				WithDelay(5 * time.Nanosecond)
		})
		assert.True(t, retrypolicy.IsExceededError(err))
		assert.Equal(t, []time.Duration{5, 5, 5, 5}, delays)
	})

	t.Run("backoff then DelayFunc", func(t *testing.T) {
		delays, _, err := runAndRecordDelays(func(b retrypolicy.Builder[any]) retrypolicy.Builder[any] {
			return b.WithBackoff(50*time.Millisecond, 100*time.Millisecond).
				WithDelayFunc(delayFunc)
		})
		assert.True(t, retrypolicy.IsExceededError(err))
		assert.Equal(t, nanoDelayFunc, delays)
	})

	t.Run("DelayFunc then fixed delay", func(t *testing.T) {
		delays, _, err := runAndRecordDelays(func(b retrypolicy.Builder[any]) retrypolicy.Builder[any] {
			return b.WithDelayFunc(delayFunc).
				WithDelay(5 * time.Nanosecond)
		})
		assert.True(t, retrypolicy.IsExceededError(err))
		assert.Equal(t, []time.Duration{5, 5, 5, 5}, delays)
	})

	t.Run("DelayFunc then random delay", func(t *testing.T) {
		delays, _, err := runAndRecordDelays(func(b retrypolicy.Builder[any]) retrypolicy.Builder[any] {
			return b.WithDelayFunc(delayFunc).
				WithRandomDelay(time.Nanosecond, 10*time.Nanosecond)
		})
		assert.True(t, retrypolicy.IsExceededError(err))
		for i, d := range delays {
			assert.GreaterOrEqual(t, d, time.Nanosecond, "delay %d should not contain a stale DelayFunc delay", i)
			assert.LessOrEqual(t, d, 10*time.Nanosecond, "delay %d should not contain a stale DelayFunc delay", i)
		}
	})

	t.Run("random then DelayFunc", func(t *testing.T) {
		delays, _, err := runAndRecordDelays(func(b retrypolicy.Builder[any]) retrypolicy.Builder[any] {
			return b.WithRandomDelay(time.Nanosecond, 10*time.Nanosecond).
				WithDelayFunc(delayFunc)
		})
		assert.True(t, retrypolicy.IsExceededError(err))
		assert.Equal(t, nanoDelayFunc, delays)
	})

	t.Run("fixed then backoff then random delay", func(t *testing.T) {
		delays, _, err := runAndRecordDelays(func(b retrypolicy.Builder[any]) retrypolicy.Builder[any] {
			return b.WithDelay(9*time.Nanosecond).
				WithBackoffFactor(time.Nanosecond, 4*time.Nanosecond, 2).
				WithRandomDelay(time.Nanosecond, 10*time.Nanosecond)
		})
		assert.True(t, retrypolicy.IsExceededError(err))
		for i, d := range delays {
			assert.GreaterOrEqual(t, d, time.Nanosecond, "delay %d should not contain a stale delay", i)
			assert.LessOrEqual(t, d, 10*time.Nanosecond, "delay %d should not contain a stale delay", i)
		}
	})

	t.Run("backoff then random then DelayFunc", func(t *testing.T) {
		delays, _, err := runAndRecordDelays(func(b retrypolicy.Builder[any]) retrypolicy.Builder[any] {
			return b.WithBackoffFactor(time.Nanosecond, 4*time.Nanosecond, 2).
				WithRandomDelay(time.Nanosecond, 10*time.Nanosecond).
				WithDelayFunc(delayFunc)
		})
		assert.True(t, retrypolicy.IsExceededError(err))
		assert.Equal(t, nanoDelayFunc, delays)
	})
}

// runAndRecordDelays runs a policy that retries 4 times, recording the scheduled delays via the public
// OnRetryScheduled listener, and returns the delays, the attempt count, and the execution error.
func runAndRecordDelays(configure func(retrypolicy.Builder[any]) retrypolicy.Builder[any]) ([]time.Duration, int, error) {
	var delays []time.Duration
	builder := configure(retrypolicy.NewBuilder[any]()).
		WithMaxRetries(4).
		OnRetryScheduled(func(event failsafe.ExecutionScheduledEvent[any]) {
			delays = append(delays, event.Delay)
		})
	attempts := 0
	err := failsafe.With(builder.Build()).Run(func() error {
		attempts++
		return testutil.ErrConnecting
	})
	return delays, attempts, err
}
