package cli

import (
	"context"
	"errors"
	"time"

	"gozillo/internal/zillow"
)

type retryPolicy struct {
	Retries int
	Backoff time.Duration
	Sleep   func(context.Context, time.Duration) error
	OnRetry func(attempt, maxAttempts int, delay time.Duration, err error)
}

func wholeLocationRetryPolicy(discovery bool, policy retryPolicy) retryPolicy {
	if discovery {
		// Discovery applies this retry budget to each bootstrap/page request so
		// completed routes are retained. Replaying the whole location would nest
		// the same backoff and repeat already successful requests.
		policy.Retries = 0
	}
	return policy
}

func (policy retryPolicy) validate() error {
	if policy.Retries < 0 {
		return errors.New("location-retries must not be negative")
	}
	if policy.Backoff < 0 {
		return errors.New("retry-backoff must not be negative")
	}
	return nil
}

func fetchWithRetry[T any](ctx context.Context, policy retryPolicy, fetch func() (T, error)) (T, error) {
	var zero T
	if policy.Sleep == nil {
		policy.Sleep = sleepContext
	}
	for attempt := 0; ; attempt++ {
		result, err := fetch()
		if err == nil {
			return result, nil
		}
		if attempt >= policy.Retries || !retryableZillowError(err) {
			return zero, err
		}
		delay := exponentialBackoff(policy.Backoff, attempt)
		var rateLimit *zillow.RateLimitError
		if errors.As(err, &rateLimit) && rateLimit.RetryAfter > delay {
			delay = rateLimit.RetryAfter
		}
		if policy.OnRetry != nil {
			policy.OnRetry(attempt+2, policy.Retries+1, delay, err)
		}
		if err := policy.Sleep(ctx, delay); err != nil {
			return zero, err
		}
	}
}

func retryableZillowError(err error) bool {
	return errors.Is(err, zillow.ErrChallenge) || errors.Is(err, zillow.ErrRateLimited)
}

func exponentialBackoff(base time.Duration, attempt int) time.Duration {
	if base <= 0 {
		return 0
	}
	delay := base
	for range attempt {
		if delay >= 5*time.Minute/2 {
			return 5 * time.Minute
		}
		delay *= 2
	}
	if delay > 5*time.Minute {
		return 5 * time.Minute
	}
	return delay
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
