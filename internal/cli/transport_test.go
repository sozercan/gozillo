package cli

import (
	"context"
	"io"
	"net/http"
	"runtime"
	"strings"
	"testing"
	"time"

	gozillosession "gozillo/internal/session"
	"gozillo/internal/zillow"
)

type cliRoundTripperFunc func(*http.Request) (*http.Response, error)

func (function cliRoundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestNewZillowClientUsesExplicitUserAgent(t *testing.T) {
	t.Parallel()

	var userAgent string
	transport := cliRoundTripperFunc(func(request *http.Request) (*http.Response, error) {
		userAgent = request.UserAgent()
		body := `<script id="__NEXT_DATA__" type="application/json">{"props":{"pageProps":{"componentProps":{"zpid":"123","gdpClientCache":"{\"query\":{\"property\":{\"zpid\":123}}}"}}}}</script>`
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"text/html"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    request,
		}, nil
	})
	client, err := newZillowClient(
		"test-version",
		time.Second,
		"",
		&http.Client{Transport: transport},
		zillow.WithUserAgent("Mozilla/5.0 Explicit Test"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.FetchProperty(context.Background(), "https://www.zillow.com/homedetails/example/123_zpid/"); err != nil {
		t.Fatal(err)
	}
	if userAgent != "Mozilla/5.0 Explicit Test" {
		t.Fatalf("User-Agent = %q, want explicit value", userAgent)
	}
}

func TestSessionCookiesDoNotImpersonateCapturedBrowserUserAgent(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("owner-only plaintext sessions are unsupported on Windows")
	}
	t.Setenv("GOZILLO_CONFIG_DIR", t.TempDir())
	store, err := gozillosession.DefaultStore()
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save("default", &gozillosession.Session{
		Version:   gozillosession.Version,
		CreatedAt: time.Now().UTC(),
		UserAgent: "Mozilla/5.0 Captured Browser",
		Cookies: []gozillosession.Cookie{{
			Name: "zgsession", Value: "test-only", Domain: "www.zillow.com", Path: "/", Secure: true,
		}},
	}); err != nil {
		t.Fatal(err)
	}

	var userAgent string
	transport := cliRoundTripperFunc(func(request *http.Request) (*http.Response, error) {
		userAgent = request.UserAgent()
		body := `<script id="__NEXT_DATA__" type="application/json">{"props":{"pageProps":{"componentProps":{"zpid":"123","gdpClientCache":"{\"query\":{\"property\":{\"zpid\":123}}}"}}}}</script>`
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{"text/html"}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    request,
		}, nil
	})
	client, err := newZillowClient("test-version", time.Second, "default", &http.Client{Transport: transport})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.FetchProperty(context.Background(), "https://www.zillow.com/homedetails/example/123_zpid/"); err != nil {
		t.Fatal(err)
	}
	if userAgent != "gozillo/test-version" {
		t.Fatalf("User-Agent = %q, want honest gozillo identity", userAgent)
	}

}

func TestNetworkOptionsSetIgnoresWhitespace(t *testing.T) {
	t.Parallel()

	if networkOptionsSet("", " \t") {
		t.Fatal("networkOptionsSet() = true for empty values")
	}
	if !networkOptionsSet("", "chrome_146") {
		t.Fatal("networkOptionsSet() = false for a configured value")
	}
}

func TestParseProxyURLValidatesSchemeAndHost(t *testing.T) {
	t.Parallel()

	for _, value := range []string{
		"http://proxy.example:8080",
		"https://proxy.example:8443",
		"socks5://proxy.example:1080",
		"socks5h://proxy.example:1080",
	} {
		if _, err := parseProxyURL(value); err != nil {
			t.Errorf("parseProxyURL(%q) error = %v", value, err)
		}
	}
	for _, value := range []string{"file:///tmp/proxy", "http://", "proxy.example:8080"} {
		if _, err := parseProxyURL(value); err == nil {
			t.Errorf("parseProxyURL(%q) error = nil", value)
		}
	}
}

func TestParseBrowserHeaders(t *testing.T) {
	t.Parallel()

	headers, err := parseBrowserHeaders([]string{
		"Accept-Language: en-US,en;q=0.9",
		`Sec-CH-UA: "Chromium";v="123"`,
		"Accept-Language: fr;q=0.5",
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := headers.Values("Accept-Language"); len(got) != 2 || got[0] != "en-US,en;q=0.9" || got[1] != "fr;q=0.5" {
		t.Fatalf("Accept-Language values = %#v", got)
	}
	if got := headers.Get("Sec-Ch-Ua"); got != `"Chromium";v="123"` {
		t.Fatalf("Sec-CH-UA = %q", got)
	}

	for _, value := range []string{"missing colon", ": value", "Name: "} {
		if _, err := parseBrowserHeaders([]string{value}); err == nil {
			t.Errorf("parseBrowserHeaders(%q) error = nil", value)
		}
	}
}
