package cli

import (
	"context"
	"errors"
	"testing"
	"time"

	"gozillo/internal/zillow"
)

func TestFetchWithRetryWaitsAndRecoversFromChallenge(t *testing.T) {
	t.Parallel()

	attempts := 0
	var delays []time.Duration
	var reportedAttempts []int
	got, err := fetchWithRetry(context.Background(), retryPolicy{
		Retries: 2,
		Backoff: 3 * time.Second,
		Sleep: func(_ context.Context, delay time.Duration) error {
			delays = append(delays, delay)
			return nil
		},
		OnRetry: func(attempt, maxAttempts int, delay time.Duration, err error) {
			if maxAttempts != 3 || err == nil {
				t.Fatalf("retry callback = attempt %d/%d, delay %s, err %v", attempt, maxAttempts, delay, err)
			}
			reportedAttempts = append(reportedAttempts, attempt)
		},
	}, func() (string, error) {
		attempts++
		if attempts < 3 {
			return "", &zillow.ChallengeError{StatusCode: 403, Reason: "test"}
		}
		return "ok", nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if got != "ok" || attempts != 3 {
		t.Fatalf("result = %q, attempts = %d", got, attempts)
	}
	if len(delays) != 2 || delays[0] != 3*time.Second || delays[1] != 6*time.Second {
		t.Fatalf("delays = %v", delays)
	}
	if len(reportedAttempts) != 2 || reportedAttempts[0] != 2 || reportedAttempts[1] != 3 {
		t.Fatalf("reported attempts = %v", reportedAttempts)
	}
}

func TestFetchWithRetryDoesNotRetrySchemaErrors(t *testing.T) {
	t.Parallel()

	attempts := 0
	want := errors.New("schema failed")
	_, err := fetchWithRetry(context.Background(), retryPolicy{Retries: 3}, func() (string, error) {
		attempts++
		return "", want
	})
	if !errors.Is(err, want) || attempts != 1 {
		t.Fatalf("error = %v, attempts = %d", err, attempts)
	}
}

func TestWholeLocationRetryPolicyDisablesNestedDiscoveryRetries(t *testing.T) {
	t.Parallel()

	base := retryPolicy{Retries: 2, Backoff: time.Minute}
	discovery := wholeLocationRetryPolicy(true, base)
	if discovery.Retries != 0 || discovery.Backoff != base.Backoff {
		t.Fatalf("discovery policy = %+v", discovery)
	}
	plain := wholeLocationRetryPolicy(false, base)
	if plain.Retries != base.Retries || plain.Backoff != base.Backoff {
		t.Fatalf("plain policy = %+v", plain)
	}
	if base.Retries != 2 {
		t.Fatalf("base policy was mutated: %+v", base)
	}
}
