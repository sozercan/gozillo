// Package httpclient contains adapters for HTTP client implementations used by
// gozillo.
package httpclient

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
	tlsclient "github.com/bogdanfinn/tls-client"
	"github.com/bogdanfinn/tls-client/profiles"
)

const minimumTLSTimeout = time.Millisecond

// TLSOptions configures a TLSRoundTripper.
//
// Profile is required. It is matched case-insensitively against
// profiles.MappedTLSClients; "default" selects profiles.DefaultClientProfile.
// Timeout must be at least one millisecond. ProxyURL is optional.
type TLSOptions struct {
	Profile  string
	Timeout  time.Duration
	ProxyURL string
}

// TLSRoundTripper adapts tls-client's fhttp request and response types to the
// standard library's http.RoundTripper interface.
type TLSRoundTripper struct {
	client tlsBackend
}

var _ http.RoundTripper = (*TLSRoundTripper)(nil)

type tlsBackend interface {
	Do(*fhttp.Request) (*fhttp.Response, error)
	CloseIdleConnections()
}

type tlsClientConfig struct {
	ProfileName             string
	Profile                 profiles.ClientProfile
	Timeout                 time.Duration
	TimeoutMilliseconds     int
	ProxyURL                string
	FollowRedirects         bool
	RandomExtensionOrdering bool
}

type tlsBackendFactory func(tlsClientConfig) (tlsBackend, error)

// NewTLSRoundTripper creates a standard-library RoundTripper backed by
// tls-client. Redirects are deliberately disabled inside tls-client so the
// outer net/http.Client remains responsible for redirect policy.
func NewTLSRoundTripper(options TLSOptions) (*TLSRoundTripper, error) {
	return newTLSRoundTripper(options, newTLSBackend)
}

func newTLSRoundTripper(options TLSOptions, factory tlsBackendFactory) (*TLSRoundTripper, error) {
	if factory == nil {
		return nil, errors.New("new TLS round tripper: backend factory is nil")
	}

	profileName, profile, err := resolveTLSProfile(options.Profile)
	if err != nil {
		return nil, fmt.Errorf("new TLS round tripper: %w", err)
	}

	timeoutMilliseconds, err := validateTLSTimeout(options.Timeout)
	if err != nil {
		return nil, fmt.Errorf("new TLS round tripper: %w", err)
	}

	proxyURL, err := validateTLSProxyURL(options.ProxyURL)
	if err != nil {
		return nil, fmt.Errorf("new TLS round tripper: %w", err)
	}

	client, err := factory(tlsClientConfig{
		ProfileName:             profileName,
		Profile:                 profile,
		Timeout:                 options.Timeout,
		TimeoutMilliseconds:     timeoutMilliseconds,
		ProxyURL:                proxyURL,
		FollowRedirects:         false,
		RandomExtensionOrdering: randomExtensionOrdering(profile),
	})
	if err != nil {
		return nil, fmt.Errorf("new TLS round tripper: create tls-client backend: %w", err)
	}
	if client == nil {
		return nil, errors.New("new TLS round tripper: create tls-client backend: returned nil client")
	}

	return &TLSRoundTripper{client: client}, nil
}

func newTLSBackend(configuration tlsClientConfig) (tlsBackend, error) {
	options := []tlsclient.HttpClientOption{
		tlsclient.WithClientProfile(configuration.Profile),
		tlsclient.WithTimeoutMilliseconds(configuration.TimeoutMilliseconds),
	}
	if configuration.ProxyURL != "" {
		options = append(options, tlsclient.WithProxyUrl(configuration.ProxyURL))
	}
	if configuration.RandomExtensionOrdering {
		options = append(options, tlsclient.WithRandomTLSExtensionOrder())
	}
	if !configuration.FollowRedirects {
		options = append(options, tlsclient.WithNotFollowRedirects())
	}

	return tlsclient.NewHttpClient(nil, options...)
}

func resolveTLSProfile(name string) (string, profiles.ClientProfile, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", profiles.ClientProfile{}, errors.New("TLS profile must not be empty")
	}
	if strings.EqualFold(name, "default") {
		return "default", profiles.DefaultClientProfile, nil
	}

	names := make([]string, 0, len(profiles.MappedTLSClients))
	for mappedName := range profiles.MappedTLSClients {
		names = append(names, mappedName)
	}
	sort.Slice(names, func(i, j int) bool {
		left := strings.ToLower(names[i])
		right := strings.ToLower(names[j])
		if left == right {
			return names[i] < names[j]
		}
		return left < right
	})

	for _, mappedName := range names {
		if strings.EqualFold(name, mappedName) {
			return mappedName, profiles.MappedTLSClients[mappedName], nil
		}
	}

	return "", profiles.ClientProfile{}, fmt.Errorf("unknown TLS profile %q", name)
}

func randomExtensionOrdering(profile profiles.ClientProfile) bool {
	clientName := profile.GetClientHelloId().Client
	return strings.EqualFold(clientName, "chrome") || strings.EqualFold(clientName, "brave")
}

func validateTLSTimeout(timeout time.Duration) (int, error) {
	if timeout <= 0 {
		return 0, errors.New("TLS timeout must be positive")
	}
	if timeout < minimumTLSTimeout {
		return 0, fmt.Errorf("TLS timeout must be at least %s", minimumTLSTimeout)
	}

	milliseconds := timeout.Milliseconds()
	maxInt := int64(^uint(0) >> 1)
	if milliseconds > maxInt {
		return 0, errors.New("TLS timeout is too large for tls-client")
	}
	return int(milliseconds), nil
}

func validateTLSProxyURL(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}

	parsed, err := url.Parse(value)
	if err != nil {
		return "", fmt.Errorf("parse TLS proxy URL: %w", err)
	}
	if parsed.Opaque != "" {
		return "", errors.New("TLS proxy URL must use hierarchical URL syntax")
	}

	parsed.Scheme = strings.ToLower(parsed.Scheme)
	switch parsed.Scheme {
	case "http", "https", "socks4", "socks4a", "socks5", "socks5h":
	case "":
		return "", errors.New("TLS proxy URL must include a scheme")
	default:
		return "", fmt.Errorf("unsupported TLS proxy scheme %q", parsed.Scheme)
	}
	if parsed.Host == "" || parsed.Hostname() == "" {
		return "", errors.New("TLS proxy URL must include a host")
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return "", errors.New("TLS proxy URL must not include a path")
	}
	if parsed.RawQuery != "" || parsed.ForceQuery {
		return "", errors.New("TLS proxy URL must not include a query")
	}
	if parsed.Fragment != "" {
		return "", errors.New("TLS proxy URL must not include a fragment")
	}
	if parsed.User != nil && parsed.User.Username() == "" {
		return "", errors.New("TLS proxy URL username must not be empty")
	}
	port := parsed.Port()
	if strings.HasPrefix(parsed.Scheme, "socks") && port == "" {
		return "", errors.New("TLS SOCKS proxy URL must include a port")
	}
	if port != "" {
		portNumber, err := strconv.Atoi(port)
		if err != nil || portNumber < 1 || portNumber > 65535 {
			return "", errors.New("TLS proxy URL port must be between 1 and 65535")
		}
	}

	return parsed.String(), nil
}

// RoundTrip implements http.RoundTripper.
func (transport *TLSRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	if transport == nil || transport.client == nil {
		return nil, errors.New("tls-client round tripper is not initialized")
	}
	if request == nil {
		return nil, errors.New("tls-client round tripper received a nil request")
	}
	if request.URL == nil {
		return nil, errors.New("tls-client round tripper received a request with a nil URL")
	}

	convertedRequest := toFHTTPRequest(request)
	response, err := transport.client.Do(convertedRequest)
	if err != nil {
		closeFHTTPResponse(response)
		if contextErr := request.Context().Err(); contextErr != nil {
			return nil, contextErr
		}
		return nil, err
	}
	if response == nil {
		return nil, errors.New("tls-client backend returned a nil response without an error")
	}

	return toHTTPResponse(response, request, convertedRequest), nil
}

// CloseIdleConnections closes idle connections held by the tls-client backend.
func (transport *TLSRoundTripper) CloseIdleConnections() {
	if transport == nil || transport.client == nil {
		return
	}
	transport.client.CloseIdleConnections()
}

func toFHTTPRequest(request *http.Request) *fhttp.Request {
	converted := &fhttp.Request{
		Method:           request.Method,
		URL:              cloneURL(request.URL),
		Proto:            request.Proto,
		ProtoMajor:       request.ProtoMajor,
		ProtoMinor:       request.ProtoMinor,
		Header:           toFHTTPHeader(request.Header),
		Body:             request.Body,
		GetBody:          request.GetBody,
		ContentLength:    request.ContentLength,
		TransferEncoding: append([]string(nil), request.TransferEncoding...),
		Close:            request.Close,
		Host:             request.Host,
		Trailer:          fhttp.Header(request.Trailer),
		Cancel:           request.Cancel,
	}
	return converted.WithContext(request.Context())
}

func toFHTTPHeader(header http.Header) fhttp.Header {
	converted := make(fhttp.Header, len(header)+1)
	names := make([]string, 0, len(header))
	for name, values := range header {
		if isFHTTPOrderKey(name) {
			continue
		}
		converted[name] = append([]string(nil), values...)
		names = append(names, name)
	}

	order := orderedHeaderNames(names)
	converted[fhttp.HeaderOrderKey] = order
	return converted
}

var preferredBrowserHeaderOrder = []string{
	"cache-control",
	"pragma",
	"sec-ch-ua",
	"sec-ch-ua-mobile",
	"sec-ch-ua-platform",
	"sec-ch-ua-platform-version",
	"sec-ch-ua-arch",
	"sec-ch-ua-bitness",
	"sec-ch-ua-full-version",
	"sec-ch-ua-full-version-list",
	"upgrade-insecure-requests",
	"user-agent",
	"dnt",
	"sec-gpc",
	"accept",
	"sec-fetch-site",
	"sec-fetch-mode",
	"sec-fetch-user",
	"sec-fetch-dest",
	"accept-encoding",
	"accept-language",
	"priority",
	"cookie",
}

func orderedHeaderNames(names []string) []string {
	byLower := make(map[string][]string, len(names))
	for _, name := range names {
		lower := strings.ToLower(name)
		byLower[lower] = append(byLower[lower], name)
	}

	order := make([]string, 0, len(names))
	for _, preferred := range preferredBrowserHeaderOrder {
		matching := byLower[preferred]
		if len(matching) == 0 {
			continue
		}
		sort.Strings(matching)
		for range matching {
			order = append(order, preferred)
		}
		delete(byLower, preferred)
	}

	remaining := make([]string, 0, len(byLower))
	for lower, matching := range byLower {
		for range matching {
			remaining = append(remaining, lower)
		}
	}
	sort.Strings(remaining)
	return append(order, remaining...)
}

func isFHTTPOrderKey(name string) bool {
	return strings.EqualFold(name, fhttp.HeaderOrderKey) || strings.EqualFold(name, fhttp.PHeaderOrderKey)
}

func toHTTPResponse(response *fhttp.Response, originalRequest *http.Request, convertedRequest *fhttp.Request) *http.Response {
	body := response.Body
	if body == nil {
		body = http.NoBody
	}

	converted := &http.Response{
		Status:           response.Status,
		StatusCode:       response.StatusCode,
		Proto:            response.Proto,
		ProtoMajor:       response.ProtoMajor,
		ProtoMinor:       response.ProtoMinor,
		Header:           fromFHTTPHeader(response.Header),
		Body:             body,
		ContentLength:    response.ContentLength,
		TransferEncoding: append([]string(nil), response.TransferEncoding...),
		Close:            response.Close,
		Uncompressed:     response.Uncompressed,
		Trailer:          http.Header(response.Trailer),
	}

	if response.Request == nil || sameFHTTPRequest(response.Request, convertedRequest) {
		converted.Request = originalRequest
	} else {
		converted.Request = fromFHTTPRequest(response.Request)
	}
	return converted
}

func sameFHTTPRequest(left *fhttp.Request, right *fhttp.Request) bool {
	if left == nil || right == nil {
		return left == right
	}
	if left == right {
		return true
	}
	return left.Method == right.Method && left.URL == right.URL
}

func fromFHTTPRequest(request *fhttp.Request) *http.Request {
	if request == nil {
		return nil
	}

	converted := &http.Request{
		Method:           request.Method,
		URL:              cloneURL(request.URL),
		Proto:            request.Proto,
		ProtoMajor:       request.ProtoMajor,
		ProtoMinor:       request.ProtoMinor,
		Header:           fromFHTTPHeader(request.Header),
		Body:             request.Body,
		GetBody:          request.GetBody,
		ContentLength:    request.ContentLength,
		TransferEncoding: append([]string(nil), request.TransferEncoding...),
		Close:            request.Close,
		Host:             request.Host,
		Trailer:          http.Header(request.Trailer),
		Cancel:           request.Cancel,
	}
	return converted.WithContext(request.Context())
}

func fromFHTTPHeader(header fhttp.Header) http.Header {
	if header == nil {
		return nil
	}

	converted := make(http.Header, len(header))
	for name, values := range header {
		if isFHTTPOrderKey(name) {
			continue
		}
		converted[name] = append([]string(nil), values...)
	}
	return converted
}

func cloneURL(source *url.URL) *url.URL {
	if source == nil {
		return nil
	}
	clone := *source
	if source.User != nil {
		user := *source.User
		clone.User = &user
	}
	return &clone
}

func closeFHTTPResponse(response *fhttp.Response) {
	if response == nil || response.Body == nil {
		return
	}
	_ = response.Body.Close()
}
