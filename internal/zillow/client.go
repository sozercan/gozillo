package zillow

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode"
)

const (
	// DefaultTimeout bounds a complete HTTP exchange, including reading its body.
	DefaultTimeout = 20 * time.Second
	// DefaultMaxResponseBytes bounds decoded response bodies held in memory.
	DefaultMaxResponseBytes int64 = 8 << 20
)

type responseKind uint8

const (
	responseJSON responseKind = iota
	responseHTML
)

// ClientOptions configures a Client. Zero values select conservative defaults.
type ClientOptions struct {
	HTTPClient       *http.Client
	Timeout          time.Duration
	MaxResponseBytes int64
	RequestID        func() uint64
	UserAgent        string
	BrowserHeaders   http.Header
	InitialCookies   []*http.Cookie
}

// ClientOption customizes a Client.
type ClientOption func(*ClientOptions) error

// WithHTTPClient supplies a transport/client template. It is copied before use.
func WithHTTPClient(client *http.Client) ClientOption {
	return func(options *ClientOptions) error {
		if client == nil {
			return errors.New("zillow HTTP client is nil")
		}
		options.HTTPClient = client
		return nil
	}
}

// WithTimeout sets the complete-request timeout.
func WithTimeout(timeout time.Duration) ClientOption {
	return func(options *ClientOptions) error {
		if timeout <= 0 {
			return errors.New("zillow timeout must be positive")
		}
		options.Timeout = timeout
		return nil
	}
}

// WithMaxResponseBytes sets the maximum response body size.
func WithMaxResponseBytes(limit int64) ClientOption {
	return func(options *ClientOptions) error {
		if limit <= 0 {
			return errors.New("zillow maximum response size must be positive")
		}
		options.MaxResponseBytes = limit
		return nil
	}
}

// WithRequestIDGenerator supplies request IDs for search calls.
func WithRequestIDGenerator(generator func() uint64) ClientOption {
	return func(options *ClientOptions) error {
		if generator == nil {
			return errors.New("zillow request ID generator is nil")
		}
		options.RequestID = generator
		return nil
	}
}

// WithInitialCookies seeds the in-memory jar from a user-imported session.
func WithInitialCookies(cookies []*http.Cookie) ClientOption {
	return func(options *ClientOptions) error {
		options.InitialCookies = make([]*http.Cookie, 0, len(cookies))
		for _, cookie := range cookies {
			if cookie == nil || strings.TrimSpace(cookie.Name) == "" {
				return errors.New("zillow initial cookie is invalid")
			}
			clone := *cookie
			options.InitialCookies = append(options.InitialCookies, &clone)
		}
		return nil
	}
}

// WithUserAgent sets an explicit User-Agent for library callers that need one.
func WithUserAgent(userAgent string) ClientOption {
	return func(options *ClientOptions) error {
		userAgent = strings.TrimSpace(userAgent)
		if userAgent == "" {
			return errors.New("zillow User-Agent must not be empty")
		}
		if len(userAgent) > 1024 || strings.ContainsAny(userAgent, "\r\n") {
			return errors.New("zillow User-Agent is invalid")
		}
		options.UserAgent = userAgent
		return nil
	}
}

var allowedBrowserHeaders = map[string]struct{}{
	"accept":                      {},
	"accept-language":             {},
	"cache-control":               {},
	"dnt":                         {},
	"pragma":                      {},
	"priority":                    {},
	"sec-ch-ua":                   {},
	"sec-ch-ua-arch":              {},
	"sec-ch-ua-bitness":           {},
	"sec-ch-ua-full-version":      {},
	"sec-ch-ua-full-version-list": {},
	"sec-ch-ua-mobile":            {},
	"sec-ch-ua-model":             {},
	"sec-ch-ua-platform":          {},
	"sec-ch-ua-platform-version":  {},
	"sec-fetch-dest":              {},
	"sec-fetch-mode":              {},
	"sec-fetch-site":              {},
	"sec-fetch-user":              {},
	"sec-gpc":                     {},
	"upgrade-insecure-requests":   {},
}

// WithBrowserHeaders sets explicit non-credential browser navigation headers
// for HTML requests. Credential-bearing and request-routing headers are not
// accepted; User-Agent, Origin, Referer, cookies, and content headers remain
// under the client's control.
func WithBrowserHeaders(headers http.Header) ClientOption {
	return func(options *ClientOptions) error {
		if len(headers) == 0 {
			options.BrowserHeaders = nil
			return nil
		}
		if len(headers) > 32 {
			return errors.New("zillow browser headers contain too many fields")
		}

		cloned := make(http.Header, len(headers))
		totalBytes := 0
		for name, values := range headers {
			normalized := strings.ToLower(strings.TrimSpace(name))
			if _, allowed := allowedBrowserHeaders[normalized]; !allowed {
				return fmt.Errorf("zillow browser header %q is not allowed", name)
			}
			if len(values) == 0 || len(values) > 8 {
				return fmt.Errorf("zillow browser header %q has an invalid value count", name)
			}
			canonical := http.CanonicalHeaderKey(normalized)
			for _, value := range values {
				if value == "" || len(value) > 4096 || strings.ContainsAny(value, "\r\n") {
					return fmt.Errorf("zillow browser header %q has an invalid value", name)
				}
				totalBytes += len(canonical) + len(value)
				if totalBytes > 16<<10 {
					return errors.New("zillow browser headers are too large")
				}
				cloned[canonical] = append(cloned[canonical], value)
			}
		}
		options.BrowserHeaders = cloned
		return nil
	}
}

// Client performs bounded, cookie-aware requests to exact Zillow hosts.
type Client struct {
	httpClient       *http.Client
	userAgent        string
	browserHeaders   http.Header
	maxResponseBytes int64
	requestID        func() uint64
}

// NewClient creates a Zillow client with an honest gozillo/<version> user agent.
func NewClient(version string, options ...ClientOption) (*Client, error) {
	configuration := ClientOptions{}
	for _, option := range options {
		if option == nil {
			return nil, errors.New("new zillow client: option is nil")
		}
		if err := option(&configuration); err != nil {
			return nil, fmt.Errorf("new zillow client: %w", err)
		}
	}
	return NewClientWithOptions(version, configuration)
}

// NewClientWithOptions creates a Zillow client from a configuration struct.
func NewClientWithOptions(version string, options ClientOptions) (*Client, error) {
	version = strings.TrimSpace(version)
	if version == "" {
		return nil, errors.New("new zillow client: version must not be empty")
	}
	for _, r := range version {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return nil, errors.New("new zillow client: version must not contain whitespace or control characters")
		}
	}

	timeout := options.Timeout
	if timeout == 0 {
		timeout = DefaultTimeout
	}
	if timeout < 0 {
		return nil, errors.New("new zillow client: timeout must be positive")
	}

	maxResponseBytes := options.MaxResponseBytes
	if maxResponseBytes == 0 {
		maxResponseBytes = DefaultMaxResponseBytes
	}
	if maxResponseBytes < 0 {
		return nil, errors.New("new zillow client: maximum response size must be positive")
	}

	var sequence atomic.Uint64
	requestID := options.RequestID
	if requestID == nil {
		requestID = func() uint64 { return sequence.Add(1) }
	}

	clientTemplate := options.HTTPClient
	if clientTemplate == nil {
		clientTemplate = &http.Client{}
	}
	clientCopy := *clientTemplate
	clientCopy.Timeout = timeout

	if clientCopy.Jar == nil {
		jar, err := cookiejar.New(nil)
		if err != nil {
			return nil, fmt.Errorf("new zillow client: create cookie jar: %w", err)
		}
		clientCopy.Jar = jar
	}
	if len(options.InitialCookies) > 0 {
		root, _ := url.Parse("https://www.zillow.com/")
		clientCopy.Jar.SetCookies(root, options.InitialCookies)
	}
	clientCopy.CheckRedirect = safeRedirectPolicy(clientTemplate.CheckRedirect)

	userAgent := options.UserAgent
	if userAgent == "" {
		userAgent = "gozillo/" + version
	}
	return &Client{
		httpClient:       &clientCopy,
		userAgent:        userAgent,
		browserHeaders:   options.BrowserHeaders.Clone(),
		maxResponseBytes: maxResponseBytes,
		requestID:        requestID,
	}, nil
}

func safeRedirectPolicy(next func(*http.Request, []*http.Request) error) func(*http.Request, []*http.Request) error {
	return func(request *http.Request, via []*http.Request) error {
		if err := validateRequestURL(request.URL); err != nil {
			return err
		}
		if len(via) >= 10 {
			return errors.New("stopped after 10 redirects")
		}
		if next != nil {
			return next(request, via)
		}
		return nil
	}
}

type requestSpec struct {
	operation string
	method    string
	url       *url.URL
	referer   string
	body      []byte
	kind      responseKind
}

func (c *Client) execute(ctx context.Context, spec requestSpec) ([]byte, http.Header, error) {
	if c == nil || c.httpClient == nil {
		return nil, nil, errors.New("zillow client is nil")
	}
	if ctx == nil {
		return nil, nil, errors.New("zillow request context is nil")
	}
	if err := validateRequestURL(spec.url); err != nil {
		return nil, nil, err
	}

	var body io.Reader
	if spec.body != nil {
		body = bytes.NewReader(spec.body)
	}
	request, err := http.NewRequestWithContext(ctx, spec.method, spec.url.String(), body)
	if err != nil {
		return nil, nil, fmt.Errorf("create Zillow %s request: %w", spec.operation, err)
	}
	if spec.kind == responseHTML {
		for name, values := range c.browserHeaders {
			request.Header[name] = append([]string(nil), values...)
		}
	}
	if request.Header.Get("Accept") == "" {
		request.Header.Set("Accept", acceptFor(spec.kind))
	}
	request.Header.Set("User-Agent", c.userAgent)
	if spec.body != nil {
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Origin", spec.url.Scheme+"://"+spec.url.Host)
	}
	if spec.referer != "" {
		request.Header.Set("Referer", spec.referer)
	}

	response, err := c.httpClient.Do(request)
	if err != nil {
		return nil, nil, fmt.Errorf("perform Zillow %s request: %w", spec.operation, err)
	}
	defer response.Body.Close()

	data, err := readBounded(response.Body, c.maxResponseBytes)
	if err != nil {
		if errors.Is(err, ErrResponseTooLarge) {
			return nil, nil, &ResponseTooLargeError{URL: spec.url.String(), Limit: c.maxResponseBytes}
		}
		return nil, nil, fmt.Errorf("read Zillow %s response: %w", spec.operation, err)
	}

	if blocked := strings.TrimSpace(response.Header.Get("X-Px-Blocked")); blocked != "" && !strings.EqualFold(blocked, "false") && blocked != "0" {
		return nil, nil, &ChallengeError{
			URL:        spec.url.String(),
			StatusCode: response.StatusCode,
			Reason:     "x-px-blocked response header",
		}
	}
	if response.StatusCode == http.StatusTooManyRequests {
		raw := strings.TrimSpace(response.Header.Get("Retry-After"))
		return nil, nil, &RateLimitError{
			URL:           spec.url.String(),
			RetryAfter:    parseRetryAfter(raw, time.Now()),
			RetryAfterRaw: raw,
		}
	}

	contentType := response.Header.Get("Content-Type")
	if challengeReason := detectChallenge(spec.kind, response.StatusCode, contentType, data); challengeReason != "" {
		return nil, nil, &ChallengeError{
			URL:        spec.url.String(),
			StatusCode: response.StatusCode,
			Reason:     challengeReason,
		}
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, nil, &HTTPStatusError{
			URL:        spec.url.String(),
			StatusCode: response.StatusCode,
			Status:     response.Status,
			Body:       responseSnippet(data),
		}
	}

	if spec.kind == responseJSON && !isJSONResponse(contentType, data) {
		return nil, nil, &SchemaDriftError{
			Operation: spec.operation,
			Path:      "response",
			Detail:    fmt.Sprintf("expected JSON content, received %q", normalizedContentType(contentType)),
		}
	}
	return data, response.Header.Clone(), nil
}

func validateRequestURL(target *url.URL) error {
	if target == nil {
		return errors.New("zillow request URL is nil")
	}
	_, err := parseAllowedZillowURL(target.String())
	return err
}

func acceptFor(kind responseKind) string {
	if kind == responseHTML {
		return "text/html,application/xhtml+xml"
	}
	return "application/json"
}

func readBounded(reader io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, ErrResponseTooLarge
	}
	return data, nil
}

func normalizedContentType(value string) string {
	if value == "" {
		return ""
	}
	mediaType, _, err := mime.ParseMediaType(value)
	if err != nil {
		return strings.ToLower(strings.TrimSpace(strings.SplitN(value, ";", 2)[0]))
	}
	return strings.ToLower(mediaType)
}

func isJSONResponse(contentType string, body []byte) bool {
	mediaType := normalizedContentType(contentType)
	if mediaType == "application/json" || strings.HasSuffix(mediaType, "+json") {
		return true
	}
	trimmed := bytes.TrimSpace(body)
	return len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[')
}

func isHTMLResponse(contentType string, body []byte) bool {
	mediaType := normalizedContentType(contentType)
	if mediaType == "text/html" || mediaType == "application/xhtml+xml" {
		return true
	}
	trimmed := bytes.TrimSpace(bytes.TrimPrefix(body, []byte{0xef, 0xbb, 0xbf}))
	lower := bytes.ToLower(trimmed)
	return bytes.HasPrefix(lower, []byte("<!doctype html")) ||
		bytes.HasPrefix(lower, []byte("<html")) ||
		bytes.HasPrefix(lower, []byte("<head")) ||
		bytes.HasPrefix(lower, []byte("<body"))
}

func detectChallenge(kind responseKind, statusCode int, contentType string, body []byte) string {
	lower := strings.ToLower(string(body))
	htmlResponse := isHTMLResponse(contentType, body)
	hasNextData := strings.Contains(lower, "__next_data__")

	// A successfully rendered property page can legitimately mention challenge
	// terms in listing text or bundled scripts. Its application structure is a
	// stronger signal than arbitrary substrings.
	if kind == responseHTML && statusCode >= http.StatusOK && statusCode < http.StatusMultipleChoices && htmlResponse && hasNextData {
		return ""
	}

	trimmed := bytes.TrimSpace(bytes.TrimPrefix(body, []byte{0xef, 0xbb, 0xbf}))
	jsonShaped := len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[')
	challengeShaped := statusCode >= http.StatusBadRequest ||
		(kind == responseJSON && (htmlResponse || !jsonShaped)) ||
		(kind == responseHTML && htmlResponse && !hasNextData)
	if challengeShaped {
		markers := []struct {
			value  string
			reason string
		}{
			{value: "px-captcha", reason: "PerimeterX CAPTCHA page"},
			{value: "press & hold", reason: "press-and-hold challenge page"},
			{value: "verify you are human", reason: "human verification page"},
			{value: "access to this page has been denied", reason: "access-denied challenge page"},
			{value: "cf-chl-", reason: "browser challenge page"},
			{value: "challenge-platform", reason: "browser challenge page"},
			{value: "robot check", reason: "robot-check challenge page"},
			{value: "captcha", reason: "CAPTCHA page"},
		}
		for _, marker := range markers {
			if strings.Contains(lower, marker.value) {
				return marker.reason
			}
		}
	}

	if kind == responseJSON && htmlResponse {
		return "HTML returned by JSON endpoint"
	}
	if kind == responseHTML && statusCode == http.StatusForbidden && htmlResponse {
		return "forbidden HTML response"
	}
	return ""
}

func parseRetryAfter(raw string, now time.Time) time.Duration {
	if raw == "" {
		return 0
	}
	if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil && seconds >= 0 {
		return time.Duration(seconds) * time.Second
	}
	if retryAt, err := http.ParseTime(raw); err == nil {
		delay := retryAt.Sub(now)
		if delay > 0 {
			return delay
		}
	}
	return 0
}

func responseSnippet(body []byte) string {
	const limit = 256
	fields := strings.Fields(string(body))
	value := strings.Join(fields, " ")
	if len(value) > limit {
		value = value[:limit] + "..."
	}
	return value
}
