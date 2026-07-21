package cli

import (
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"gozillo/internal/httpclient"
	gozillosession "gozillo/internal/session"
	"gozillo/internal/zillow"
)

type zillowTransportOptions struct {
	Timeout        time.Duration
	ProxyURL       string
	SessionName    string
	TLSProfile     string
	UserAgent      string
	BrowserHeaders http.Header
}

func networkOptionsSet(values ...string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

func parseBrowserHeaders(values []string) (http.Header, error) {
	headers := make(http.Header, len(values))
	for _, raw := range values {
		name, value, ok := strings.Cut(raw, ":")
		name = strings.TrimSpace(name)
		value = strings.TrimSpace(value)
		if !ok || name == "" || value == "" {
			return nil, fmt.Errorf("browser header %q must use Name: Value", raw)
		}
		headers.Add(name, value)
	}
	return headers, nil
}

func newZillowTransport(version string, options zillowTransportOptions) (*zillow.Client, error) {
	profile := strings.TrimSpace(options.TLSProfile)
	if profile == "" {
		return nil, errors.New("tls-client profile is required for network requests")
	}

	proxyURL, err := tlsProxyURL(options.ProxyURL)
	if err != nil {
		return nil, err
	}
	roundTripper, err := httpclient.NewTLSRoundTripper(httpclient.TLSOptions{
		Profile:  profile,
		Timeout:  options.Timeout,
		ProxyURL: proxyURL,
	})
	if err != nil {
		return nil, fmt.Errorf("create tls-client transport: %w", err)
	}
	httpClient := &http.Client{Transport: roundTripper}

	extraOptions := make([]zillow.ClientOption, 0, 2)
	if strings.TrimSpace(options.UserAgent) != "" {
		extraOptions = append(extraOptions, zillow.WithUserAgent(options.UserAgent))
	}
	if len(options.BrowserHeaders) > 0 {
		extraOptions = append(extraOptions, zillow.WithBrowserHeaders(options.BrowserHeaders))
	}
	return newZillowClient(version, options.Timeout, options.SessionName, httpClient, extraOptions...)
}

func tlsProxyURL(explicit string) (string, error) {
	if strings.TrimSpace(explicit) != "" {
		parsed, err := parseProxyURL(explicit)
		if err != nil {
			return "", err
		}
		return parsed.String(), nil
	}

	target, _ := url.Parse("https://www.zillow.com/")
	proxyURL, err := http.ProxyFromEnvironment(&http.Request{URL: target})
	if err != nil {
		return "", fmt.Errorf("resolve proxy from environment: %w", err)
	}
	if proxyURL == nil {
		return "", nil
	}
	if _, err := validateProxyURL(proxyURL); err != nil {
		return "", fmt.Errorf("proxy from environment: %w", err)
	}
	return proxyURL.String(), nil
}

func parseProxyURL(value string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return nil, fmt.Errorf("parse proxy URL: %w", err)
	}
	return validateProxyURL(parsed)
}

func validateProxyURL(parsed *url.URL) (*url.URL, error) {
	if parsed == nil {
		return nil, errors.New("proxy URL is nil")
	}
	if parsed.Host == "" {
		return nil, errors.New("proxy URL must include a host")
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "socks5", "socks5h":
	default:
		return nil, fmt.Errorf("unsupported proxy scheme %q", parsed.Scheme)
	}
	return parsed, nil
}

func newZillowClient(version string, timeout time.Duration, sessionName string, httpClient *http.Client, extraOptions ...zillow.ClientOption) (*zillow.Client, error) {
	options := []zillow.ClientOption{
		zillow.WithHTTPClient(httpClient),
		zillow.WithTimeout(timeout),
	}
	options = append(options, extraOptions...)
	if strings.TrimSpace(sessionName) != "" {
		store, err := gozillosession.DefaultStore()
		if err != nil {
			return nil, err
		}
		loaded, err := store.Load(sessionName)
		if err != nil {
			return nil, err
		}
		options = append(options, zillow.WithInitialCookies(loaded.HTTPCookies()))
	}
	return zillow.NewClient(version, options...)
}
