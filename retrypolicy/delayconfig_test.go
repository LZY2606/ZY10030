package retrypolicy

import (
	"errors"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/failsafe-go/failsafe-go"
	"github.com/failsafe-go/failsafe-go/internal/testutil"
)

// delayMode is a mutually exclusive retry delay mode along with the deterministic delay sequence it is expected to
// produce for 5 retries.
type delayMode struct {
	name  string
	apply func(b Builder[any]) Builder[any]
	want  []time.Duration
}

func testDelayModes() []delayMode {
	ns := time.Nanosecond
	return []delayMode{
		{
			name:  "fixed",
			apply: func(b Builder[any]) Builder[any] { return b.WithDelay(100 * ns) },
			want:  []time.Duration{100, 100, 100, 100, 100},
		},
		{
			name:  "backoff",
			apply: func(b Builder[any]) Builder[any] { return b.WithBackoff(1*ns, 8*ns) },
			want:  []time.Duration{1, 2, 4, 8, 8},
		},
		{
			// A degenerate min == max range keeps the random mode deterministic
			name:  "random",
			apply: func(b Builder[any]) Builder[any] { return b.WithRandomDelay(50*ns, 50*ns) },
			want:  []time.Duration{50, 50, 50, 50, 50},
		},
		{
			name: "delayfunc",
			apply: func(b Builder[any]) Builder[any] {
				return b.WithDelayFunc(func(exec failsafe.ExecutionAttempt[any]) time.Duration {
					return time.Duration(exec.Retries()+1) * 10 * ns
				})
			},
			want: []time.Duration{10, 20, 30, 40, 50},
		},
	}
}

// computeDelays records the delay that each retry would be scheduled with, using the policy's delay computation
// directly so that no actual sleeping takes place.
func computeDelays(rp RetryPolicy[any], retries int) []time.Duration {
	e := &executor[any]{retryPolicy: rp.(*retryPolicy[any])}
	exec := &testutil.TestExecution[any]{}
	delays := make([]time.Duration, 0, retries)
	for i := 0; i < retries; i++ {
		delays = append(delays, e.getDelay(exec))
		exec.TheRetries++
	}
	return delays
}

// TestDelayModeSwitching asserts that the fixed, backoff, random, and DelayFunc delay modes are mutually exclusive:
// switching modes on the same builder replaces the previous mode in full, regardless of configuration order, so no
// state from a previously configured mode can leak into the computed delays.
func TestDelayModeSwitching(t *testing.T) {
	modes := testDelayModes()
	byName := map[string]delayMode{}
	for _, m := range modes {
		byName[m.name] = m
	}

	type chain struct {
		name  string
		steps []string
	}
	var chains []chain
	// Every ordered pair of distinct modes
	for _, first := range modes {
		for _, second := range modes {
			if first.name != second.name {
				chains = append(chains, chain{first.name + "->" + second.name, []string{first.name, second.name}})
			}
		}
	}
	// Three-step mixed chains
	chains = append(chains,
		chain{"fixed->backoff->random", []string{"fixed", "backoff", "random"}},
		chain{"random->backoff->fixed", []string{"random", "backoff", "fixed"}},
		chain{"backoff->delayfunc->backoff", []string{"backoff", "delayfunc", "backoff"}},
		chain{"delayfunc->random->delayfunc", []string{"delayfunc", "random", "delayfunc"}},
		chain{"fixed->random->delayfunc", []string{"fixed", "random", "delayfunc"}},
	)

	for _, tc := range chains {
		t.Run(tc.name, func(t *testing.T) {
			builder := NewBuilder[any]()
			for _, step := range tc.steps {
				builder = byName[step].apply(builder)
			}
			want := byName[tc.steps[len(tc.steps)-1]].want
			assert.Equal(t, want, computeDelays(builder.Build(), len(want)))
		})
	}

	// Reconfiguring the same mode replaces its parameters rather than composing them
	t.Run("backoff->backoff replaces parameters", func(t *testing.T) {
		rp := NewBuilder[any]().
			WithBackoff(time.Nanosecond, 8*time.Nanosecond).
			WithBackoff(2*time.Nanosecond, 16*time.Nanosecond).
			Build()
		assert.Equal(t, []time.Duration{2, 4, 8, 16, 16}, computeDelays(rp, 5))
	})

	t.Run("fixed->fixed replaces parameters", func(t *testing.T) {
		rp := NewBuilder[any]().
			WithDelay(100 * time.Nanosecond).
			WithDelay(7 * time.Nanosecond).
			Build()
		assert.Equal(t, []time.Duration{7, 7, 7, 7, 7}, computeDelays(rp, 5))
	})
}

// TestDelayModeSwitchingObservedViaListener proves, through the public OnRetryScheduled listener and the execution
// result, that the delays a policy actually schedules match the most recently configured delay mode. Delays are
// nanosecond scale so the execution does not meaningfully sleep.
func TestDelayModeSwitchingObservedViaListener(t *testing.T) {
	ns := time.Nanosecond
	testErr := errors.New("test")
	testCases := []struct {
		name      string
		configure func(b Builder[any]) Builder[any]
		want      []time.Duration
	}{
		{
			name: "backoff->random",
			configure: func(b Builder[any]) Builder[any] {
				return b.WithBackoff(1*ns, 8*ns).WithRandomDelay(50*ns, 50*ns)
			},
			want: []time.Duration{50, 50, 50, 50},
		},
		{
			name: "random->fixed",
			configure: func(b Builder[any]) Builder[any] {
				return b.WithRandomDelay(50*ns, 50*ns).WithDelay(100 * ns)
			},
			want: []time.Duration{100, 100, 100, 100},
		},
		{
			name: "fixed->backoff->delayfunc",
			configure: func(b Builder[any]) Builder[any] {
				return b.WithDelay(100*ns).
					WithBackoff(1*ns, 8*ns).
					WithDelayFunc(func(exec failsafe.ExecutionAttempt[any]) time.Duration {
						return time.Duration(exec.Retries()+1) * 10 * ns
					})
			},
			want: []time.Duration{10, 20, 30, 40},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var delays []time.Duration
			rp := tc.configure(NewBuilder[any]()).
				WithMaxRetries(len(tc.want)).
				OnRetryScheduled(func(e failsafe.ExecutionScheduledEvent[any]) {
					mu.Lock()
					defer mu.Unlock()
					delays = append(delays, e.Delay)
				}).
				Build()

			err := failsafe.With(rp).Run(func() error {
				return testErr
			})

			// The execution result proves retries were exhausted as configured
			assert.True(t, IsExceededError(err))
			mu.Lock()
			defer mu.Unlock()
			assert.Equal(t, tc.want, delays)
		})
	}
}

// TestDelayModeEdgeCases covers zero delays, max delay clamping, and backoff overflow at the time.Duration boundary.
func TestDelayModeEdgeCases(t *testing.T) {
	t.Run("zero fixed delay replaces backoff", func(t *testing.T) {
		rp := NewBuilder[any]().
			WithBackoff(time.Nanosecond, 8*time.Nanosecond).
			WithDelay(0).
			Build()
		assert.Equal(t, []time.Duration{0, 0, 0, 0, 0}, computeDelays(rp, 5))
	})

	t.Run("default builder has no delay", func(t *testing.T) {
		assert.Equal(t, []time.Duration{0, 0, 0, 0, 0}, computeDelays(NewBuilder[any]().Build(), 5))
	})

	t.Run("zero random delay range means no delay", func(t *testing.T) {
		rp := NewBuilder[any]().
			WithDelay(100*time.Nanosecond).
			WithRandomDelay(0, 0).
			Build()
		assert.Equal(t, []time.Duration{0, 0, 0, 0, 0}, computeDelays(rp, 5))
	})

	t.Run("backoff clamps to maxDelay after switching from random", func(t *testing.T) {
		rp := NewBuilder[any]().
			WithRandomDelay(50*time.Nanosecond, 50*time.Nanosecond).
			WithBackoff(2*time.Nanosecond, 4*time.Nanosecond).
			Build()
		assert.Equal(t, []time.Duration{2, 4, 4, 4, 4}, computeDelays(rp, 5))
	})

	t.Run("backoff overflow clamps to maxDelay", func(t *testing.T) {
		rp := NewBuilder[any]().
			WithBackoffFactor(math.MaxInt64/2, math.MaxInt64, 4).
			Build()
		want := []time.Duration{math.MaxInt64 / 2, math.MaxInt64, math.MaxInt64, math.MaxInt64, math.MaxInt64}
		assert.Equal(t, want, computeDelays(rp, 5))
	})

	t.Run("delay clamped to remaining max duration", func(t *testing.T) {
		rp := NewBuilder[any]().
			WithDelay(time.Second).
			WithMaxDuration(100 * time.Millisecond).
			Build()
		e := &executor[any]{retryPolicy: rp.(*retryPolicy[any])}
		exec := &testutil.TestExecution[any]{TheElapsedTime: 90 * time.Millisecond}
		assert.Equal(t, 10*time.Millisecond, e.getDelay(exec))
		exec.TheElapsedTime = 150 * time.Millisecond
		assert.Equal(t, time.Duration(0), e.getDelay(exec))
	})
}

// TestDelayModeRandomRange asserts that a switched-to random delay mode stays within its configured range and does
// not leak the previously configured fixed delay.
func TestDelayModeRandomRange(t *testing.T) {
	rp := NewBuilder[any]().
		WithDelay(100*time.Nanosecond).
		WithRandomDelay(10*time.Nanosecond, 20*time.Nanosecond).
		Build()
	for _, delay := range computeDelays(rp, 100) {
		assert.GreaterOrEqual(t, delay, 10*time.Nanosecond)
		assert.LessOrEqual(t, delay, 20*time.Nanosecond)
	}
}

// TestDelayModeJitterIsOrthogonal asserts that jitter, whether configured before or after a mode switch, applies to
// the final delay mode only and does not resurrect previously configured modes.
func TestDelayModeJitterIsOrthogonal(t *testing.T) {
	t.Run("jitter applies to switched-to fixed delay", func(t *testing.T) {
		rp := NewBuilder[any]().
			WithBackoff(time.Nanosecond, 8*time.Nanosecond).
			WithDelay(100 * time.Nanosecond).
			WithJitter(10 * time.Nanosecond).
			Build()
		for _, delay := range computeDelays(rp, 50) {
			assert.GreaterOrEqual(t, delay, 90*time.Nanosecond)
			assert.LessOrEqual(t, delay, 110*time.Nanosecond)
		}
	})

	t.Run("jitter configured before switch applies to random delay", func(t *testing.T) {
		rp := NewBuilder[any]().
			WithJitter(5*time.Nanosecond).
			WithDelay(100*time.Nanosecond).
			WithRandomDelay(50*time.Nanosecond, 50*time.Nanosecond).
			Build()
		for _, delay := range computeDelays(rp, 50) {
			assert.GreaterOrEqual(t, delay, 45*time.Nanosecond)
			assert.LessOrEqual(t, delay, 55*time.Nanosecond)
		}
	})

	t.Run("jitter factor applies to switched-to backoff delay", func(t *testing.T) {
		rp := NewBuilder[any]().
			WithRandomDelay(50*time.Nanosecond, 50*time.Nanosecond).
			WithBackoff(100*time.Nanosecond, 100*time.Nanosecond).
			WithJitterFactor(0.5).
			Build()
		for _, delay := range computeDelays(rp, 50) {
			assert.GreaterOrEqual(t, delay, 50*time.Nanosecond)
			assert.LessOrEqual(t, delay, 150*time.Nanosecond)
		}
	})
}

// TestBuilderSnapshotIsolation asserts that policies built from the same builder are snapshots: reconfiguring the
// builder after a Build does not rewrite previously built policies.
func TestBuilderSnapshotIsolation(t *testing.T) {
	t.Run("delay mode", func(t *testing.T) {
		builder := NewBuilder[any]().WithDelay(100 * time.Nanosecond)
		p1 := builder.Build()

		builder.WithRandomDelay(50*time.Nanosecond, 50*time.Nanosecond).WithMaxRetries(3)
		p2 := builder.Build()

		assert.Equal(t, []time.Duration{100, 100, 100}, computeDelays(p1, 3))
		assert.Equal(t, []time.Duration{50, 50, 50}, computeDelays(p2, 3))
	})

	t.Run("failure conditions", func(t *testing.T) {
		builder := NewBuilder[any]().HandleResult("a")
		p1 := builder.Build()

		builder.HandleResult("b")
		p2 := builder.Build()

		assert.True(t, p1.(*retryPolicy[any]).IsFailure("a", nil))
		assert.False(t, p1.(*retryPolicy[any]).IsFailure("b", nil))
		assert.True(t, p2.(*retryPolicy[any]).IsFailure("a", nil))
		assert.True(t, p2.(*retryPolicy[any]).IsFailure("b", nil))
	})
}

// TestBuiltPoliciesConcurrentIsolation asserts that policies built from the same builder can be used concurrently,
// with results depending only on their own configuration and executions, not on shared counters, random state, or
// residue from the builder's configuration chain.
func TestBuiltPoliciesConcurrentIsolation(t *testing.T) {
	ns := time.Nanosecond
	var mu sync.Mutex
	var delays1, delays2 []time.Duration

	builder := NewBuilder[any]().
		WithDelay(100 * ns).
		WithMaxRetries(3).
		OnRetryScheduled(func(e failsafe.ExecutionScheduledEvent[any]) {
			mu.Lock()
			defer mu.Unlock()
			delays1 = append(delays1, e.Delay)
		})
	p1 := builder.Build()

	builder.WithBackoff(1*ns, 4*ns).
		OnRetryScheduled(func(e failsafe.ExecutionScheduledEvent[any]) {
			mu.Lock()
			defer mu.Unlock()
			delays2 = append(delays2, e.Delay)
		})
	p2 := builder.Build()

	const goroutines = 8
	testErr := errors.New("test")
	var attempts1, attempts2 atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < goroutines; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_ = failsafe.With(p1).Run(func() error {
				attempts1.Add(1)
				return testErr
			})
		}()
		go func() {
			defer wg.Done()
			_ = failsafe.With(p2).Run(func() error {
				attempts2.Add(1)
				return testErr
			})
		}()
	}
	wg.Wait()

	// Each policy performs 1 initial attempt plus its own 3 retries per execution
	assert.Equal(t, int32(4*goroutines), attempts1.Load())
	assert.Equal(t, int32(4*goroutines), attempts2.Load())

	var wantDelays1, wantDelays2 []time.Duration
	for i := 0; i < goroutines; i++ {
		wantDelays1 = append(wantDelays1, 100, 100, 100)
		wantDelays2 = append(wantDelays2, 1, 2, 4)
	}
	assert.ElementsMatch(t, wantDelays1, delays1)
	assert.ElementsMatch(t, wantDelays2, delays2)
}
