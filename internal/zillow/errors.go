package zillow

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	// ErrHostNotAllowed identifies URLs outside the explicit Zillow host allowlist.
	ErrHostNotAllowed = errors.New("zillow host is not allowed")
	// ErrChallenge identifies an anti-bot or browser challenge response.
	ErrChallenge = errors.New("zillow browser challenge detected")
	// ErrRateLimited identifies an HTTP 429 response.
	ErrRateLimited = errors.New("zillow rate limited the request")
	// ErrSchemaDrift identifies a response that no longer matches the expected shape.
	ErrSchemaDrift = errors.New("zillow response schema drift")
	// ErrResponseTooLarge identifies a response that exceeded the configured limit.
	ErrResponseTooLarge = errors.New("zillow response exceeded the size limit")
)

// HostNotAllowedError reports a URL host rejected by the client safety policy.
type HostNotAllowedError struct {
	Host string
}

func (e *HostNotAllowedError) Error() string {
	if e == nil || e.Host == "" {
		return ErrHostNotAllowed.Error()
	}
	return fmt.Sprintf("%s: %q", ErrHostNotAllowed, e.Host)
}

func (e *HostNotAllowedError) Unwrap() error { return ErrHostNotAllowed }

// ChallengeError reports an anti-bot or browser-only response.
type ChallengeError struct {
	URL        string
	StatusCode int
	Reason     string
}

func (e *ChallengeError) Error() string {
	if e == nil {
		return ErrChallenge.Error()
	}

	parts := []string{ErrChallenge.Error()}
	if e.StatusCode != 0 {
		parts = append(parts, fmt.Sprintf("status %d", e.StatusCode))
	}
	if e.Reason != "" {
		parts = append(parts, e.Reason)
	}
	if e.URL != "" {
		parts = append(parts, e.URL)
	}
	return strings.Join(parts, ": ")
}

func (e *ChallengeError) Unwrap() error { return ErrChallenge }

// RateLimitError reports a 429 response and its Retry-After value, when present.
type RateLimitError struct {
	URL           string
	RetryAfter    time.Duration
	RetryAfterRaw string
}

func (e *RateLimitError) Error() string {
	if e == nil {
		return ErrRateLimited.Error()
	}

	message := ErrRateLimited.Error()
	if e.RetryAfterRaw != "" {
		message += ": retry after " + e.RetryAfterRaw
	}
	if e.URL != "" {
		message += ": " + e.URL
	}
	return message
}

func (e *RateLimitError) Unwrap() error { return ErrRateLimited }

// SchemaDriftError reports a missing or incompatible response field.
type SchemaDriftError struct {
	Operation string
	Path      string
	Detail    string
}

func (e *SchemaDriftError) Error() string {
	if e == nil {
		return ErrSchemaDrift.Error()
	}

	message := ErrSchemaDrift.Error()
	if e.Operation != "" {
		message += " in " + e.Operation
	}
	if e.Path != "" {
		message += " at " + e.Path
	}
	if e.Detail != "" {
		message += ": " + e.Detail
	}
	return message
}

func (e *SchemaDriftError) Unwrap() error { return ErrSchemaDrift }

// ResponseTooLargeError reports the configured response limit.
type ResponseTooLargeError struct {
	URL   string
	Limit int64
}

func (e *ResponseTooLargeError) Error() string {
	if e == nil {
		return ErrResponseTooLarge.Error()
	}

	message := ErrResponseTooLarge.Error()
	if e.Limit > 0 {
		message += fmt.Sprintf(": limit %d bytes", e.Limit)
	}
	if e.URL != "" {
		message += ": " + e.URL
	}
	return message
}

func (e *ResponseTooLargeError) Unwrap() error { return ErrResponseTooLarge }

// HTTPStatusError reports a non-success status that was not classified more specifically.
type HTTPStatusError struct {
	URL        string
	StatusCode int
	Status     string
	Body       string
}

func (e *HTTPStatusError) Error() string {
	if e == nil {
		return "zillow request failed"
	}

	status := e.Status
	if status == "" && e.StatusCode != 0 {
		status = fmt.Sprintf("status %d", e.StatusCode)
	}
	message := "zillow request failed"
	if status != "" {
		message += ": " + status
	}
	if e.URL != "" {
		message += ": " + e.URL
	}
	return message
}
