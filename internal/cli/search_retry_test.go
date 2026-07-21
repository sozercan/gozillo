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
	got, err := fetchWithRetry(context.Background(), retryPolicy{
		Retries: 2,
		Backoff: 3 * time.Second,
		Sleep: func(_ context.Context, delay time.Duration) error {
			delays = append(delays, delay)
			return nil
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
