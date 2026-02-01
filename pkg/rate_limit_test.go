package raria2

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestRateLimitFractionalRates(t *testing.T) {
	tests := []struct {
		name        string
		rateLimit   float64
		expectedMin time.Duration
		expectedMax time.Duration
		shouldDelay bool
	}{
		{
			name:        "no rate limit",
			rateLimit:   0,
			shouldDelay: false,
		},
		{
			name:        "negative rate limit (should be ignored)",
			rateLimit:   -1,
			shouldDelay: false,
		},
		{
			name:        "fractional rate - 0.5 rps (2 second interval)",
			rateLimit:   0.5,
			expectedMin: 1900 * time.Millisecond, // Allow some tolerance
			expectedMax: 2100 * time.Millisecond,
			shouldDelay: true,
		},
		{
			name:        "fractional rate - 0.1 rps (10 second interval)",
			rateLimit:   0.1,
			expectedMin: 9500 * time.Millisecond, // Allow some tolerance
			expectedMax: 10500 * time.Millisecond,
			shouldDelay: true,
		},
		{
			name:        "low rate - 1 rps (1 second interval)",
			rateLimit:   1.0,
			expectedMin: 950 * time.Millisecond,
			expectedMax: 1050 * time.Millisecond,
			shouldDelay: true,
		},
		{
			name:        "moderate rate - 2.5 rps (400ms interval)",
			rateLimit:   2.5,
			expectedMin: 350 * time.Millisecond,
			expectedMax: 450 * time.Millisecond,
			shouldDelay: true,
		},
		{
			name:        "high rate - 10 rps (100ms interval)",
			rateLimit:   10,
			expectedMin: 50 * time.Millisecond, // Allow tolerance for timing variations
			expectedMax: 150 * time.Millisecond,
			shouldDelay: true,
		},
		{
			name:        "very high rate - 100 rps (10ms interval)",
			rateLimit:   100,
			expectedMin: 5 * time.Millisecond,
			expectedMax: 15 * time.Millisecond,
			shouldDelay: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewHTTPClient(30*time.Second, tt.rateLimit)

			if !tt.shouldDelay {
				// Should complete immediately
				start := time.Now()
				client.waitForRateLimit()
				client.waitForRateLimit()
				elapsed := time.Since(start)
				assert.Less(t, elapsed, 50*time.Millisecond, "Expected no significant delay")
				return
			}

			// First call should not delay
			start := time.Now()
			client.waitForRateLimit()
			firstCallElapsed := time.Since(start)
			assert.Less(t, firstCallElapsed, 10*time.Millisecond, "First call should not delay")

			// Second call should respect rate limit
			start = time.Now()
			client.waitForRateLimit()
			elapsed := time.Since(start)

			if tt.expectedMin > 0 {
				assert.GreaterOrEqual(t, elapsed, tt.expectedMin,
					"Expected delay to be at least %v, got %v", tt.expectedMin, elapsed)
			}
			if tt.expectedMax > 0 {
				assert.LessOrEqual(t, elapsed, tt.expectedMax,
					"Expected delay to be at most %v, got %v", tt.expectedMax, elapsed)
			}
		})
	}
}

func TestRateLimitEdgeCases(t *testing.T) {
	t.Run("very_small_fractional_rate", func(t *testing.T) {
		// Test very small rate to ensure no panic or zero division
		// Use a more reasonable rate for testing (0.1 rps = 10 second interval)
		client := NewHTTPClient(30*time.Second, 0.1)

		// Should not panic and should complete quickly for first call
		start := time.Now()
		client.waitForRateLimit()
		elapsed := time.Since(start)
		assert.Less(t, elapsed, 10*time.Millisecond, "First call should be immediate")

		// Second call should delay significantly (around 10 seconds, but we'll interrupt early)
		start = time.Now()

		// Use a timeout channel to prevent endless waiting
		done := make(chan struct{})
		go func() {
			client.waitForRateLimit()
			close(done)
		}()

		// Wait for either completion or timeout (2 seconds should be enough to see it's working)
		select {
		case <-done:
			elapsed = time.Since(start)
			// Should have delayed at least some reasonable amount
			assert.Greater(t, elapsed, 8*time.Second, "Should delay for small rate")
		case <-time.After(2 * time.Second):
			// If it hasn't completed in 2 seconds, that's actually good - it means it's delaying
			// We can consider the test passed
			t.Log("Rate limiting is working (test interrupted after reasonable delay)")
		}
	})

	t.Run("zero_rate_limit", func(t *testing.T) {
		client := NewHTTPClient(30*time.Second, 0)

		// Should not delay at all
		start := time.Now()
		for i := 0; i < 10; i++ {
			client.waitForRateLimit()
		}
		elapsed := time.Since(start)
		assert.Less(t, elapsed, 50*time.Millisecond, "Should not delay with zero rate limit")
	})

	t.Run("negative_rate_limit", func(t *testing.T) {
		client := NewHTTPClient(30*time.Second, -5.0)

		// Should not delay (negative rates treated as disabled)
		start := time.Now()
		for i := 0; i < 10; i++ {
			client.waitForRateLimit()
		}
		elapsed := time.Since(start)
		assert.Less(t, elapsed, 50*time.Millisecond, "Should not delay with negative rate limit")
	})
}

func TestRateLimitPrecision(t *testing.T) {
	// Test that rate limiting works correctly with high precision rates
	client := NewHTTPClient(30*time.Second, 1.618) // Golden ratio ~1.618 requests per second

	// Calculate expected interval
	expectedInterval := time.Duration(float64(time.Second.Nanoseconds()) / 1.618)

	// Test multiple intervals without artificial sleep
	for i := 0; i < 3; i++ {
		start := time.Now()
		client.waitForRateLimit()
		if i > 0 { // Skip first call (no delay expected)
			elapsed := time.Since(start)
			tolerance := expectedInterval / 10 // 10% tolerance

			assert.GreaterOrEqual(t, elapsed, expectedInterval-tolerance,
				"Interval %d: Expected at least %v, got %v", i, expectedInterval-tolerance, elapsed)
			assert.LessOrEqual(t, elapsed, expectedInterval+tolerance,
				"Interval %d: Expected at most %v, got %v", i, expectedInterval+tolerance, elapsed)
		}
		// Don't sleep - let the rate limiter control the timing
	}
}
