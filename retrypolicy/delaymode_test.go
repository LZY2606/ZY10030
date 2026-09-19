package retrypolicy

import (
	"math"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/failsafe-go/failsafe-go"
	"github.com/failsafe-go/failsafe-go/internal/testutil"
)

// delayStep applies one delay mode configuration to a Builder, in chain order.
type delayStep struct {
	name  string
	apply func(b Builder[any]) Builder[any]
}

func fixedStep(delay time.Duration) delayStep {
	return delayStep{
		name:  "fixed",
		apply: func(b Builder[any]) Builder[any] { return b.WithDelay(delay) },
	}
}

func backoffStep(delay time.Duration, maxDelay time.Duration, factor float64) delayStep {
	return delayStep{
		name:  "backoff",
		apply: func(b Builder[any]) Builder[any] { return b.WithBackoffFactor(delay, maxDelay, factor) },
	}
}

func randomStep(minDelay time.Duration, maxDelay time.Duration) delayStep {
	return delayStep{
		name:  "random",
		apply: func(b Builder[any]) Builder[any] { return b.WithRandomDelay(minDelay, maxDelay) },
	}
}

func delayFuncStep(fn failsafe.DelayFunc[any]) delayStep {
	return delayStep{
		name:  "delayfunc",
		apply: func(b Builder[any]) Builder[any] { return b.WithDelayFunc(fn) },
	}
}

// The delay modes used in the matrix. Values are chosen so that no mode's possible delays overlap another mode's,
// which lets a stale mode be detected by range alone.
var (
	matrixFixed     = fixedStep(100 * time.Millisecond)                          // 100ms
	matrixBackoff   = backoffStep(100*time.Millisecond, 250*time.Millisecond, 2) // 100ms, 200ms, 250ms, ...
	matrixRandom    = randomStep(10*time.Millisecond, 40*time.Millisecond)       // [10ms, 40ms]
	matrixDelayFunc = delayFuncStep(func(exec failsafe.ExecutionAttempt[any]) time.Duration {
		return time.Duration(exec.Retries()+1) * 7 * time.Millisecond // 7ms, 14ms, 21ms, ...
	})
)

// delayExpectation asserts a recorded delay sequence.
type delayExpectation func(t *testing.T, delays []time.Duration)

func expectExactly(delays ...time.Duration) delayExpectation {
	return func(t *testing.T, actual []time.Duration) {
		assert.Equal(t, delays, actual, "delay sequence should match the last configured delay mode")
	}
}

func expectInRange(minDelay time.Duration, maxDelay time.Duration) delayExpectation {
	return func(t *testing.T, actual []time.Duration) {
		for i, d := range actual {
			assert.GreaterOrEqual(t, d, minDelay, "delay %d should be >= min random delay", i)
			assert.LessOrEqual(t, d, maxDelay, "delay %d should be <= max random delay", i)
		}
	}
}

// recordDelays builds a policy from the steps and records the delay computed for each retry, without sleeping.
func recordDelays(steps []delayStep, retries int) []time.Duration {
	builder := NewBuilder[any]()
	for _, step := range steps {
		builder = step.apply(builder)
	}
	return recordPolicyDelays(builder.Build().(*retryPolicy[any]), retries)
}

// delayTestExecution is a controllable execution attempt whose elapsed time is fixed, so that delay computation can
// be recorded without a real clock.
type delayTestExecution struct {
	testutil.TestExecution[any]
}

func (e delayTestExecution) ElapsedTime() time.Duration {
	return 0
}

// recordPolicyDelays records the delays that an executor for the policy computes for each retry, without sleeping.
func recordPolicyDelays(rp *retryPolicy[any], retries int) []time.Duration {
	exec := &executor[any]{retryPolicy: rp}
	attempt := &delayTestExecution{}
	delays := make([]time.Duration, 0, retries)
	for i := 0; i < retries; i++ {
		delays = append(delays, exec.getDelay(attempt))
		attempt.TheRetries++
	}
	return delays
}

func fixedExpectation() delayExpectation {
	return expectExactly(100*time.Millisecond, 100*time.Millisecond, 100*time.Millisecond, 100*time.Millisecond, 100*time.Millisecond)
}

func backoffExpectation() delayExpectation {
	return expectExactly(100*time.Millisecond, 200*time.Millisecond, 250*time.Millisecond, 250*time.Millisecond, 250*time.Millisecond)
}

func randomExpectation() delayExpectation {
	return expectInRange(10*time.Millisecond, 40*time.Millisecond)
}

func delayFuncExpectation() delayExpectation {
	return expectExactly(7*time.Millisecond, 14*time.Millisecond, 21*time.Millisecond, 28*time.Millisecond, 35*time.Millisecond)
}

// expectationFor returns the expectation for the last step of a chain.
func expectationFor(step delayStep) delayExpectation {
	switch step.name {
	case "fixed":
		return fixedExpectation()
	case "backoff":
		return backoffExpectation()
	case "random":
		return randomExpectation()
	default:
		return delayFuncExpectation()
	}
}

// TestDelayModeSwitchingMatrix verifies that delay modes are mutually exclusive: after switching modes, only the last
// configured mode may contribute to the computed delays. Covers all ordered pairs of fixed, backoff, random and
// DelayFunc modes, plus three-segment mixed chains.
func TestDelayModeSwitchingMatrix(t *testing.T) {
	modes := []delayStep{matrixFixed, matrixBackoff, matrixRandom, matrixDelayFunc}

	type testCase struct {
		name   string
		steps  []delayStep
		expect delayExpectation
	}

	var cases []testCase
	// All ordered pairs of distinct modes
	for _, first := range modes {
		for _, second := range modes {
			if first.name == second.name {
				continue
			}
			cases = append(cases, testCase{
				name:   first.name + "->" + second.name,
				steps:  []delayStep{first, second},
				expect: expectationFor(second),
			})
		}
	}
	// Three-segment mixed chains
	chains := [][]delayStep{
		{matrixFixed, matrixBackoff, matrixRandom},
		{matrixBackoff, matrixRandom, matrixDelayFunc},
		{matrixRandom, matrixDelayFunc, matrixFixed},
		{matrixDelayFunc, matrixFixed, matrixBackoff},
		{matrixRandom, matrixBackoff, matrixFixed},
		{matrixBackoff, matrixFixed, matrixDelayFunc},
	}
	for _, chain := range chains {
		name := chain[0].name
		for _, step := range chain[1:] {
			name += "->" + step.name
		}
		cases = append(cases, testCase{
			name:   name,
			steps:  chain,
			expect: expectationFor(chain[len(chain)-1]),
		})
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.expect(t, recordDelays(tc.steps, 5))
		})
	}
}

// TestDelayModeZeroDelay verifies that a final zero delay configuration is honored and no stale mode fills in delays.
func TestDelayModeZeroDelay(t *testing.T) {
	zeros := []time.Duration{0, 0, 0, 0, 0}

	t.Run("default builder has no delay", func(t *testing.T) {
		assert.Equal(t, zeros, recordDelays(nil, 5))
	})

	t.Run("random then zero fixed delay", func(t *testing.T) {
		assert.Equal(t, zeros, recordDelays([]delayStep{matrixRandom, fixedStep(0)}, 5))
	})

	t.Run("backoff then zero fixed delay", func(t *testing.T) {
		assert.Equal(t, zeros, recordDelays([]delayStep{matrixBackoff, fixedStep(0)}, 5))
	})

	t.Run("fixed then DelayFunc returning zero", func(t *testing.T) {
		assert.Equal(t, zeros, recordDelays([]delayStep{matrixFixed, delayFuncStep(func(failsafe.ExecutionAttempt[any]) time.Duration {
			return 0
		})}, 5))
	})

	t.Run("random delay with zero range", func(t *testing.T) {
		assert.Equal(t, zeros, recordDelays([]delayStep{randomStep(0, 0)}, 5))
	})
}

// TestDelayModeBackoffMaxDelay verifies that backoff delays are clamped to the max delay, including when the backoff
// computation would overflow a time.Duration.
func TestDelayModeBackoffMaxDelay(t *testing.T) {
	t.Run("clamps to max delay", func(t *testing.T) {
		backoffExpectation()(t, recordDelays([]delayStep{matrixBackoff}, 5))
	})

	t.Run("clamps without overflow", func(t *testing.T) {
		huge := time.Duration(1) << 62
		expected := []time.Duration{huge, math.MaxInt64, math.MaxInt64, math.MaxInt64, math.MaxInt64}
		delays := recordDelays([]delayStep{backoffStep(huge, math.MaxInt64, 2)}, 5)
		assert.Equal(t, expected, delays)
		for i, d := range delays {
			assert.Positive(t, d, "delay %d should never overflow to a non-positive duration", i)
		}
	})
}

// TestDelayModeJitter verifies that jitter remains an orthogonal modifier of the final delay mode, and that the two
// jitter settings replace each other as documented.
func TestDelayModeJitter(t *testing.T) {
	t.Run("jitter duration applies to final mode", func(t *testing.T) {
		steps := []delayStep{matrixBackoff, matrixRandom}
		builder := NewBuilder[any]()
		for _, step := range steps {
			builder = step.apply(builder)
		}
		builder.WithJitter(5 * time.Millisecond)
		delays := recordPolicyDelays(builder.Build().(*retryPolicy[any]), 50)
		for i, d := range delays {
			assert.GreaterOrEqual(t, d, 5*time.Millisecond, "delay %d", i)
			assert.LessOrEqual(t, d, 45*time.Millisecond, "delay %d", i)
		}
	})

	t.Run("jitter factor applies to final mode", func(t *testing.T) {
		builder := fixedStep(100 * time.Millisecond).apply(NewBuilder[any]())
		builder.WithJitterFactor(0.5)
		delays := recordPolicyDelays(builder.Build().(*retryPolicy[any]), 50)
		for i, d := range delays {
			assert.GreaterOrEqual(t, d, 50*time.Millisecond, "delay %d", i)
			assert.LessOrEqual(t, d, 150*time.Millisecond, "delay %d", i)
		}
	})

	t.Run("jitter factor replaces jitter duration", func(t *testing.T) {
		builder := fixedStep(100 * time.Millisecond).apply(NewBuilder[any]())
		builder.WithJitter(10 * time.Millisecond).WithJitterFactor(0.5)
		rp := builder.Build().(*retryPolicy[any])
		assert.Zero(t, rp.jitter, "WithJitterFactor should replace a previously configured jitter duration")
		delays := recordPolicyDelays(rp, 50)
		for i, d := range delays {
			assert.GreaterOrEqual(t, d, 50*time.Millisecond, "delay %d", i)
			assert.LessOrEqual(t, d, 150*time.Millisecond, "delay %d", i)
		}
	})

	t.Run("jitter duration replaces jitter factor", func(t *testing.T) {
		builder := fixedStep(100 * time.Millisecond).apply(NewBuilder[any]())
		builder.WithJitterFactor(0.5).WithJitter(10 * time.Millisecond)
		rp := builder.Build().(*retryPolicy[any])
		assert.Zero(t, rp.jitterFactor, "WithJitter should replace a previously configured jitter factor")
		delays := recordPolicyDelays(rp, 50)
		for i, d := range delays {
			assert.GreaterOrEqual(t, d, 90*time.Millisecond, "delay %d", i)
			assert.LessOrEqual(t, d, 110*time.Millisecond, "delay %d", i)
		}
	})
}

// TestBuilderSnapshotIsolation verifies that policies built from the same builder are snapshots: later builder
// configuration must not leak into previously built policies.
func TestBuilderSnapshotIsolation(t *testing.T) {
	t.Run("delay modes", func(t *testing.T) {
		builder := fixedStep(100 * time.Millisecond).apply(NewBuilder[any]())
		p1 := builder.Build().(*retryPolicy[any])

		matrixRandom.apply(builder)
		p2 := builder.Build().(*retryPolicy[any])

		backoffStep(200*time.Millisecond, 500*time.Millisecond, 2).apply(builder)
		p3 := builder.Build().(*retryPolicy[any])

		// Reconfigure the builder once more after all policies are built
		matrixDelayFunc.apply(builder)

		fixedExpectation()(t, recordPolicyDelays(p1, 5))
		randomExpectation()(t, recordPolicyDelays(p2, 5))
		expectExactly(200*time.Millisecond, 400*time.Millisecond, 500*time.Millisecond, 500*time.Millisecond, 500*time.Millisecond)(t, recordPolicyDelays(p3, 5))
	})

	t.Run("failure and abort conditions", func(t *testing.T) {
		builder := NewBuilder[any]().HandleErrors(testutil.ErrConnecting)
		p1 := builder.Build().(*retryPolicy[any])

		builder.HandleResult("bad")
		builder.AbortOnResult("abort")
		p2 := builder.Build().(*retryPolicy[any])

		assert.False(t, p1.IsFailure("bad", nil), "p1 should not see conditions added after it was built")
		assert.False(t, p1.IsAbortable("abort", nil), "p1 should not see abort conditions added after it was built")

		assert.True(t, p2.IsFailure("bad", nil))
		assert.True(t, p2.IsAbortable("abort", nil))
	})
}

// TestBuiltPoliciesConcurrentUse verifies that policies built from the same builder can be used concurrently, with
// results that depend only on their own configuration and execution context.
func TestBuiltPoliciesConcurrentUse(t *testing.T) {
	builder := fixedStep(100 * time.Millisecond).apply(NewBuilder[any]())
	p1 := builder.Build().(*retryPolicy[any])
	backoffStep(10*time.Millisecond, 80*time.Millisecond, 2).apply(builder)
	p2 := builder.Build().(*retryPolicy[any])

	expectedBackoff := []time.Duration{10 * time.Millisecond, 20 * time.Millisecond, 40 * time.Millisecond, 80 * time.Millisecond, 80 * time.Millisecond, 80 * time.Millisecond}

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				delays := recordPolicyDelays(p1, 6)
				for k, d := range delays {
					assert.Equal(t, 100*time.Millisecond, d, "delay %d", k)
				}
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				assert.Equal(t, expectedBackoff, recordPolicyDelays(p2, 6))
			}
		}()
	}
	wg.Wait()
}
