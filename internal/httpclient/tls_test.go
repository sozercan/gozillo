package httpclient

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	fhttp "github.com/bogdanfinn/fhttp"
	"github.com/bogdanfinn/tls-client/profiles"
)

type fakeTLSBackend struct {
	do         func(*fhttp.Request) (*fhttp.Response, error)
	closeCalls atomic.Int32
}

func (backend *fakeTLSBackend) Do(request *fhttp.Request) (*fhttp.Response, error) {
	if backend.do == nil {
		return &fhttp.Response{
			Status:     "200 OK",
			StatusCode: http.StatusOK,
			Proto:      "HTTP/1.1",
			ProtoMajor: 1,
			ProtoMinor: 1,
			Header:     make(fhttp.Header),
			Body:       fhttp.NoBody,
			Request:    request,
		}, nil
	}
	return backend.do(request)
}

func (backend *fakeTLSBackend) CloseIdleConnections() {
	backend.closeCalls.Add(1)
}

func TestNewTLSRoundTripperResolvesProfilesCaseInsensitively(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		profile       string
		canonicalName string
		wantProfile   profiles.ClientProfile
		wantRandom    bool
	}{
		{
			name:          "default alias",
			profile:       " DeFaUlT ",
			canonicalName: "default",
			wantProfile:   profiles.DefaultClientProfile,
			wantRandom:    true,
		},
		{
			name:          "chrome",
			profile:       "ChRoMe_146",
			canonicalName: "chrome_146",
			wantProfile:   profiles.Chrome_146,
			wantRandom:    true,
		},
		{
			name:          "brave psk",
			profile:       "BRAVE_146_psk",
			canonicalName: "brave_146_PSK",
			wantProfile:   profiles.Brave_146_PSK,
			wantRandom:    true,
		},
		{
			name:          "firefox psk",
			profile:       "FIREFOX_146_psk",
			canonicalName: "firefox_146_PSK",
			wantProfile:   profiles.Firefox_146_PSK,
			wantRandom:    false,
		},
		{
			name:          "safari",
			profile:       "SAFARI_IOS_18_5",
			canonicalName: "safari_ios_18_5",
			wantProfile:   profiles.Safari_IOS_18_5,
			wantRandom:    false,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			backend := &fakeTLSBackend{}
			var got tlsClientConfig
			transport, err := newTLSRoundTripper(TLSOptions{
				Profile: test.profile,
				Timeout: 1500 * time.Millisecond,
			}, func(configuration tlsClientConfig) (tlsBackend, error) {
				got = configuration
				return backend, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if transport.client != backend {
				t.Fatal("NewTLSRoundTripper did not retain the factory backend")
			}
			if got.ProfileName != test.canonicalName {
				t.Fatalf("ProfileName = %q, want %q", got.ProfileName, test.canonicalName)
			}
			if got.Profile.GetClientHelloStr() != test.wantProfile.GetClientHelloStr() {
				t.Fatalf("profile = %q, want %q", got.Profile.GetClientHelloStr(), test.wantProfile.GetClientHelloStr())
			}
			if got.RandomExtensionOrdering != test.wantRandom {
				t.Fatalf("RandomExtensionOrdering = %v, want %v", got.RandomExtensionOrdering, test.wantRandom)
			}
			if got.FollowRedirects {
				t.Fatal("FollowRedirects = true, want false")
			}
			if got.Timeout != 1500*time.Millisecond || got.TimeoutMilliseconds != 1500 {
				t.Fatalf("timeout = %s/%dms, want 1.5s/1500ms", got.Timeout, got.TimeoutMilliseconds)
			}
		})
	}
}

func TestNewTLSRoundTripperValidatesConfiguration(t *testing.T) {
	t.Parallel()

	valid := TLSOptions{Profile: "chrome_146", Timeout: time.Second}
	tests := []struct {
		name    string
		mutate  func(*TLSOptions)
		wantErr string
	}{
		{
			name: "empty profile",
			mutate: func(options *TLSOptions) {
				options.Profile = " \t"
			},
			wantErr: "TLS profile must not be empty",
		},
		{
			name: "unknown profile",
			mutate: func(options *TLSOptions) {
				options.Profile = "netscape_1"
			},
			wantErr: `unknown TLS profile "netscape_1"`,
		},
		{
			name: "zero timeout",
			mutate: func(options *TLSOptions) {
				options.Timeout = 0
			},
			wantErr: "TLS timeout must be positive",
		},
		{
			name: "negative timeout",
			mutate: func(options *TLSOptions) {
				options.Timeout = -time.Second
			},
			wantErr: "TLS timeout must be positive",
		},
		{
			name: "sub-millisecond timeout",
			mutate: func(options *TLSOptions) {
				options.Timeout = time.Microsecond
			},
			wantErr: "TLS timeout must be at least 1ms",
		},
		{
			name: "proxy without scheme",
			mutate: func(options *TLSOptions) {
				options.ProxyURL = "//proxy.example:8080"
			},
			wantErr: "TLS proxy URL must include a scheme",
		},
		{
			name: "unsupported proxy scheme",
			mutate: func(options *TLSOptions) {
				options.ProxyURL = "file:///tmp/socket"
			},
			wantErr: `unsupported TLS proxy scheme "file"`,
		},
		{
			name: "proxy without host",
			mutate: func(options *TLSOptions) {
				options.ProxyURL = "http:///"
			},
			wantErr: "TLS proxy URL must include a host",
		},
		{
			name: "proxy with path",
			mutate: func(options *TLSOptions) {
				options.ProxyURL = "http://proxy.example:8080/tunnel"
			},
			wantErr: "TLS proxy URL must not include a path",
		},
		{
			name: "proxy with query",
			mutate: func(options *TLSOptions) {
				options.ProxyURL = "http://proxy.example:8080/?mode=test"
			},
			wantErr: "TLS proxy URL must not include a query",
		},
		{
			name: "proxy with fragment",
			mutate: func(options *TLSOptions) {
				options.ProxyURL = "http://proxy.example:8080/#fragment"
			},
			wantErr: "TLS proxy URL must not include a fragment",
		},
		{
			name: "proxy with empty query",
			mutate: func(options *TLSOptions) {
				options.ProxyURL = "http://proxy.example:8080/?"
			},
			wantErr: "TLS proxy URL must not include a query",
		},
		{
			name: "SOCKS proxy without port",
			mutate: func(options *TLSOptions) {
				options.ProxyURL = "socks5://proxy.example"
			},
			wantErr: "TLS SOCKS proxy URL must include a port",
		},
		{
			name: "proxy with invalid port",
			mutate: func(options *TLSOptions) {
				options.ProxyURL = "http://proxy.example:70000"
			},
			wantErr: "port must be between 1 and 65535",
		},
		{
			name: "proxy with empty username",
			mutate: func(options *TLSOptions) {
				options.ProxyURL = "http://:password@proxy.example:8080"
			},
			wantErr: "username must not be empty",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			options := valid
			test.mutate(&options)
			factoryCalled := false
			_, err := newTLSRoundTripper(options, func(tlsClientConfig) (tlsBackend, error) {
				factoryCalled = true
				return &fakeTLSBackend{}, nil
			})
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("error = %v, want substring %q", err, test.wantErr)
			}
			if factoryCalled {
				t.Fatal("backend factory was called for invalid configuration")
			}
		})
	}
}

func TestNewTLSRoundTripperValidatesFactoryResults(t *testing.T) {
	t.Parallel()

	options := TLSOptions{Profile: "default", Timeout: time.Second}
	if _, err := newTLSRoundTripper(options, nil); err == nil || !strings.Contains(err.Error(), "backend factory is nil") {
		t.Fatalf("nil factory error = %v", err)
	}

	factoryErr := errors.New("factory failed")
	if _, err := newTLSRoundTripper(options, func(tlsClientConfig) (tlsBackend, error) {
		return nil, factoryErr
	}); !errors.Is(err, factoryErr) || !strings.Contains(err.Error(), "create tls-client backend") {
		t.Fatalf("factory error = %v", err)
	}

	if _, err := newTLSRoundTripper(options, func(tlsClientConfig) (tlsBackend, error) {
		return nil, nil
	}); err == nil || !strings.Contains(err.Error(), "returned nil client") {
		t.Fatalf("nil backend error = %v", err)
	}
}

func TestNewTLSRoundTripperNormalizesSupportedProxyURLs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  string
	}{
		{input: "", want: ""},
		{input: " HTTPS://user:pass@proxy.example:8443/ ", want: "https://user:pass@proxy.example:8443/"},
		{input: "socks4://proxy.example:1080", want: "socks4://proxy.example:1080"},
		{input: "socks4a://proxy.example:1080", want: "socks4a://proxy.example:1080"},
		{input: "socks5://proxy.example:1080", want: "socks5://proxy.example:1080"},
		{input: "socks5h://proxy.example:1080", want: "socks5h://proxy.example:1080"},
	}

	for _, test := range tests {
		test := test
		t.Run(test.want, func(t *testing.T) {
			t.Parallel()

			var got tlsClientConfig
			_, err := newTLSRoundTripper(TLSOptions{
				Profile:  "firefox_148",
				Timeout:  time.Second,
				ProxyURL: test.input,
			}, func(configuration tlsClientConfig) (tlsBackend, error) {
				got = configuration
				return &fakeTLSBackend{}, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			if got.ProxyURL != test.want {
				t.Fatalf("ProxyURL = %q, want %q", got.ProxyURL, test.want)
			}
		})
	}
}

type requestContextKey struct{}

type trackingReadCloser struct {
	reader *strings.Reader
	closed atomic.Bool
}

func newTrackingReadCloser(value string) *trackingReadCloser {
	return &trackingReadCloser{reader: strings.NewReader(value)}
}

func (body *trackingReadCloser) Read(buffer []byte) (int, error) {
	return body.reader.Read(buffer)
}

func (body *trackingReadCloser) Close() error {
	body.closed.Store(true)
	return nil
}

func TestTLSRoundTripperConvertsRequest(t *testing.T) {
	t.Parallel()

	requestURL, err := url.Parse("https://user:pass@example.test:8443/a%2Fb?q=one#fragment")
	if err != nil {
		t.Fatal(err)
	}
	body := newTrackingReadCloser("request payload")
	cancel := make(chan struct{})
	request := (&http.Request{
		Method:     http.MethodPatch,
		URL:        requestURL,
		Proto:      "HTTP/1.1",
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header: http.Header{
			"X-Zeta":              {"z1", "z2"},
			"accept":              {"application/json"},
			"X-Alpha":             nil,
			fhttp.HeaderOrderKey:  {"caller-order"},
			fhttp.PHeaderOrderKey: {":path", ":method"},
		},
		Body:          body,
		ContentLength: int64(len("request payload")),
		TransferEncoding: []string{
			"chunked",
		},
		Close:   true,
		Host:    "override.example.test",
		Trailer: http.Header{"X-Trailer": {"initial"}},
		Cancel:  cancel,
	}).WithContext(context.WithValue(context.Background(), requestContextKey{}, "context value"))
	request.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader("replay payload")), nil
	}

	backend := &fakeTLSBackend{}
	backend.do = func(converted *fhttp.Request) (*fhttp.Response, error) {
		if converted.Method != request.Method || converted.Host != request.Host {
			t.Fatalf("method/host = %q/%q, want %q/%q", converted.Method, converted.Host, request.Method, request.Host)
		}
		if converted.Context().Value(requestContextKey{}) != "context value" {
			t.Fatal("request context value was not preserved")
		}
		if converted.URL == request.URL || converted.URL.String() != request.URL.String() {
			t.Fatalf("converted URL = %#v, want an equal independent copy", converted.URL)
		}
		if converted.URL.User == request.URL.User {
			t.Fatal("URL user info was not copied")
		}
		if converted.Body != request.Body {
			t.Fatal("request body was not forwarded")
		}
		if converted.ContentLength != request.ContentLength || converted.Close != request.Close {
			t.Fatalf("content length/close = %d/%v, want %d/%v", converted.ContentLength, converted.Close, request.ContentLength, request.Close)
		}
		if !reflect.DeepEqual(converted.TransferEncoding, request.TransferEncoding) {
			t.Fatalf("TransferEncoding = %#v, want %#v", converted.TransferEncoding, request.TransferEncoding)
		}
		if converted.Cancel != request.Cancel {
			t.Fatal("legacy Cancel channel was not forwarded")
		}
		if converted.Proto != request.Proto || converted.ProtoMajor != request.ProtoMajor || converted.ProtoMinor != request.ProtoMinor {
			t.Fatal("request protocol fields were not copied")
		}

		wantHeader := fhttp.Header{
			"X-Zeta":  {"z1", "z2"},
			"accept":  {"application/json"},
			"X-Alpha": nil,
		}
		for name, values := range wantHeader {
			if !reflect.DeepEqual(converted.Header[name], values) {
				t.Fatalf("header %q = %#v, want %#v", name, converted.Header[name], values)
			}
		}
		wantOrder := []string{"accept", "x-alpha", "x-zeta"}
		if !reflect.DeepEqual(converted.Header[fhttp.HeaderOrderKey], wantOrder) {
			t.Fatalf("header order = %#v, want %#v", converted.Header[fhttp.HeaderOrderKey], wantOrder)
		}
		if _, exists := converted.Header[fhttp.PHeaderOrderKey]; exists {
			t.Fatal("caller-supplied pseudo-header order leaked into fhttp request")
		}

		replayed, err := converted.GetBody()
		if err != nil {
			t.Fatal(err)
		}
		replayBytes, err := io.ReadAll(replayed)
		if err != nil {
			t.Fatal(err)
		}
		_ = replayed.Close()
		if string(replayBytes) != "replay payload" {
			t.Fatalf("GetBody payload = %q", replayBytes)
		}

		request.Trailer.Set("X-Trailer", "updated")
		if converted.Trailer.Get("X-Trailer") != "updated" {
			t.Fatal("request trailer updates are not visible to fhttp")
		}

		converted.URL.Path = "/mutated"
		converted.Header["X-Zeta"][0] = "mutated"
		converted.TransferEncoding[0] = "mutated"

		return &fhttp.Response{
			Status:     "204 No Content",
			StatusCode: http.StatusNoContent,
			Proto:      "HTTP/1.1",
			ProtoMajor: 1,
			ProtoMinor: 1,
			Header:     make(fhttp.Header),
			Body:       fhttp.NoBody,
			Request:    converted,
		}, nil
	}

	transport := &TLSRoundTripper{client: backend}
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()

	if request.URL.Path != "/a/b" {
		t.Fatalf("original URL path mutated to %q", request.URL.Path)
	}
	if request.Header.Get("X-Zeta") != "z1" {
		t.Fatalf("original header mutated to %q", request.Header.Get("X-Zeta"))
	}
	if request.TransferEncoding[0] != "chunked" {
		t.Fatalf("original TransferEncoding mutated to %q", request.TransferEncoding[0])
	}
	if _, exists := request.Header[fhttp.HeaderOrderKey]; !exists {
		t.Fatal("adapter unexpectedly removed caller header metadata from original request")
	}
	if response.Request != request {
		t.Fatal("response request does not point to the original net/http request")
	}
}

type trailerBody struct {
	reader  *strings.Reader
	trailer fhttp.Header
	closed  atomic.Bool
}

func (body *trailerBody) Read(buffer []byte) (int, error) {
	read, err := body.reader.Read(buffer)
	if errors.Is(err, io.EOF) {
		body.trailer.Set("X-Final-Trailer", "complete")
	}
	return read, err
}

func (body *trailerBody) Close() error {
	body.closed.Store(true)
	return nil
}

func TestTLSRoundTripperConvertsResponse(t *testing.T) {
	t.Parallel()

	request, err := http.NewRequest(http.MethodGet, "https://example.test/resource", nil)
	if err != nil {
		t.Fatal(err)
	}
	trailer := fhttp.Header{"X-Final-Trailer": nil}
	body := &trailerBody{reader: strings.NewReader("response payload"), trailer: trailer}
	backendResponse := &fhttp.Response{
		Status:     "206 Partial Content",
		StatusCode: http.StatusPartialContent,
		Proto:      "HTTP/2.0",
		ProtoMajor: 2,
		ProtoMinor: 0,
		Header: fhttp.Header{
			"Content-Type":        {"text/plain"},
			"X-Multi":             {"one", "two"},
			fhttp.HeaderOrderKey:  {"x-multi", "content-type"},
			fhttp.PHeaderOrderKey: {":status"},
		},
		Body:             body,
		ContentLength:    int64(len("response payload")),
		TransferEncoding: []string{"identity"},
		Close:            true,
		Uncompressed:     true,
		Trailer:          trailer,
	}

	backend := &fakeTLSBackend{do: func(converted *fhttp.Request) (*fhttp.Response, error) {
		requestCopy := *converted
		backendResponse.Request = &requestCopy
		return backendResponse, nil
	}}
	transport := &TLSRoundTripper{client: backend}
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}

	if response.Status != backendResponse.Status || response.StatusCode != backendResponse.StatusCode {
		t.Fatalf("status = %q/%d", response.Status, response.StatusCode)
	}
	if response.Proto != backendResponse.Proto || response.ProtoMajor != 2 || response.ProtoMinor != 0 {
		t.Fatalf("protocol = %q/%d.%d", response.Proto, response.ProtoMajor, response.ProtoMinor)
	}
	if response.Header.Get("Content-Type") != "text/plain" || !reflect.DeepEqual(response.Header.Values("X-Multi"), []string{"one", "two"}) {
		t.Fatalf("headers = %#v", response.Header)
	}
	if _, exists := response.Header[fhttp.HeaderOrderKey]; exists {
		t.Fatal("fhttp header-order metadata leaked into net/http response")
	}
	if _, exists := response.Header[fhttp.PHeaderOrderKey]; exists {
		t.Fatal("fhttp pseudo-header-order metadata leaked into net/http response")
	}
	if response.Body != body {
		t.Fatal("response body was not forwarded")
	}
	if response.ContentLength != backendResponse.ContentLength || response.Close != backendResponse.Close || response.Uncompressed != backendResponse.Uncompressed {
		t.Fatal("response metadata was not copied")
	}
	if !reflect.DeepEqual(response.TransferEncoding, backendResponse.TransferEncoding) {
		t.Fatalf("TransferEncoding = %#v", response.TransferEncoding)
	}
	if response.Request != request {
		t.Fatal("response Request does not point to original request")
	}

	backendResponse.Header.Set("Content-Type", "mutated")
	backendResponse.TransferEncoding[0] = "mutated"
	if response.Header.Get("Content-Type") != "text/plain" || response.TransferEncoding[0] != "identity" {
		t.Fatal("response headers or transfer encoding alias backend storage")
	}

	payload, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if string(payload) != "response payload" {
		t.Fatalf("response payload = %q", payload)
	}
	if response.Trailer.Get("X-Final-Trailer") != "complete" {
		t.Fatalf("response trailer = %#v, want completed trailer", response.Trailer)
	}
	if err := response.Body.Close(); err != nil {
		t.Fatal(err)
	}
	if !body.closed.Load() {
		t.Fatal("closing net/http response did not close fhttp response body")
	}
}

func TestTLSRoundTripperConvertsDistinctResponseRequest(t *testing.T) {
	t.Parallel()

	original, err := http.NewRequest(http.MethodGet, "https://example.test/original", nil)
	if err != nil {
		t.Fatal(err)
	}
	responseContext := context.WithValue(context.Background(), requestContextKey{}, "response context")
	backend := &fakeTLSBackend{do: func(*fhttp.Request) (*fhttp.Response, error) {
		requestURL, err := url.Parse("https://other.example/actual")
		if err != nil {
			t.Fatal(err)
		}
		responseRequest := (&fhttp.Request{
			Method:        http.MethodHead,
			URL:           requestURL,
			Header:        fhttp.Header{"X-Response-Request": {"yes"}, fhttp.HeaderOrderKey: {"x-response-request"}},
			Host:          "host.override",
			ContentLength: 7,
			Trailer:       fhttp.Header{"X-Trailer": {"value"}},
		}).WithContext(responseContext)
		return &fhttp.Response{
			Status:     "200 OK",
			StatusCode: http.StatusOK,
			Proto:      "HTTP/1.1",
			ProtoMajor: 1,
			ProtoMinor: 1,
			Header:     make(fhttp.Header),
			Body:       fhttp.NoBody,
			Request:    responseRequest,
		}, nil
	}}

	response, err := (&TLSRoundTripper{client: backend}).RoundTrip(original)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.Request == original || response.Request == nil {
		t.Fatal("distinct fhttp response request was not converted")
	}
	if response.Request.Method != http.MethodHead || response.Request.URL.String() != "https://other.example/actual" {
		t.Fatalf("converted response request = %#v", response.Request)
	}
	if response.Request.Header.Get("X-Response-Request") != "yes" || response.Request.Host != "host.override" || response.Request.ContentLength != 7 {
		t.Fatal("converted response request fields are incomplete")
	}
	if response.Request.Context().Value(requestContextKey{}) != "response context" {
		t.Fatal("converted response request context was not preserved")
	}
	if _, exists := response.Request.Header[fhttp.HeaderOrderKey]; exists {
		t.Fatal("fhttp header-order metadata leaked into converted response request")
	}
}

func TestTLSRoundTripperPreservesContextCancellation(t *testing.T) {
	t.Parallel()

	entered := make(chan struct{})
	backend := &fakeTLSBackend{do: func(request *fhttp.Request) (*fhttp.Response, error) {
		close(entered)
		<-request.Context().Done()
		return nil, request.Context().Err()
	}}
	transport := &TLSRoundTripper{client: backend}
	contextValue := context.WithValue(context.Background(), requestContextKey{}, "preserved")
	ctx, cancel := context.WithCancel(contextValue)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.test/cancel", nil)
	if err != nil {
		t.Fatal(err)
	}

	result := make(chan error, 1)
	go func() {
		_, roundTripErr := transport.RoundTrip(request)
		result <- roundTripErr
	}()
	<-entered
	cancel()

	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("RoundTrip error = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("RoundTrip did not stop after context cancellation")
	}
}

func TestTLSRoundTripperLetsOuterHTTPClientOwnCookies(t *testing.T) {
	t.Parallel()

	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	target, err := url.Parse("https://example.test/")
	if err != nil {
		t.Fatal(err)
	}
	jar.SetCookies(target, []*http.Cookie{{Name: "seed", Value: "one", Path: "/"}})

	var calls int
	backend := &fakeTLSBackend{do: func(request *fhttp.Request) (*fhttp.Response, error) {
		calls++
		cookie := request.Header.Get("Cookie")
		if !strings.Contains(cookie, "seed=one") {
			t.Fatalf("request %d Cookie = %q, want seeded cookie", calls, cookie)
		}
		if calls == 2 && !strings.Contains(cookie, "session=two") {
			t.Fatalf("request %d Cookie = %q, want response cookie", calls, cookie)
		}
		header := make(fhttp.Header)
		if calls == 1 {
			header.Add("Set-Cookie", "session=two; Path=/; Secure; HttpOnly")
		}
		return &fhttp.Response{
			Status:     "204 No Content",
			StatusCode: http.StatusNoContent,
			Proto:      "HTTP/1.1",
			ProtoMajor: 1,
			ProtoMinor: 1,
			Header:     header,
			Body:       fhttp.NoBody,
			Request:    request,
		}, nil
	}}
	client := &http.Client{Transport: &TLSRoundTripper{client: backend}, Jar: jar}

	for range 2 {
		response, err := client.Get(target.String())
		if err != nil {
			t.Fatal(err)
		}
		if err := response.Body.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 2 {
		t.Fatalf("backend calls = %d, want 2", calls)
	}
}

func TestTLSRoundTripperLetsOuterHTTPClientHandleRedirects(t *testing.T) {
	t.Parallel()

	var calls []string
	backend := &fakeTLSBackend{do: func(request *fhttp.Request) (*fhttp.Response, error) {
		calls = append(calls, request.URL.Path)
		switch request.URL.Path {
		case "/start":
			return &fhttp.Response{
				Status:     "302 Found",
				StatusCode: http.StatusFound,
				Proto:      "HTTP/1.1",
				ProtoMajor: 1,
				ProtoMinor: 1,
				Header:     fhttp.Header{"Location": {"/final"}},
				Body:       fhttp.NoBody,
				Request:    request,
			}, nil
		case "/final":
			return &fhttp.Response{
				Status:        "200 OK",
				StatusCode:    http.StatusOK,
				Proto:         "HTTP/1.1",
				ProtoMajor:    1,
				ProtoMinor:    1,
				Header:        make(fhttp.Header),
				Body:          io.NopCloser(strings.NewReader("done")),
				ContentLength: 4,
				Request:       request,
			}, nil
		default:
			return nil, errors.New("unexpected redirect path")
		}
	}}
	client := &http.Client{Transport: &TLSRoundTripper{client: backend}}
	response, err := client.Get("https://example.test/start")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != http.StatusOK || string(payload) != "done" {
		t.Fatalf("final response = %d %q", response.StatusCode, payload)
	}
	if !reflect.DeepEqual(calls, []string{"/start", "/final"}) {
		t.Fatalf("backend calls = %#v, want outer-client redirect sequence", calls)
	}
}

func TestTLSRoundTripperErrorHandling(t *testing.T) {
	t.Parallel()

	request, err := http.NewRequest(http.MethodGet, "https://example.test/error", nil)
	if err != nil {
		t.Fatal(err)
	}

	backendErr := errors.New("backend failed")
	body := newTrackingReadCloser("discard me")
	transport := &TLSRoundTripper{client: &fakeTLSBackend{do: func(*fhttp.Request) (*fhttp.Response, error) {
		return &fhttp.Response{Body: body}, backendErr
	}}}
	if _, err := transport.RoundTrip(request); !errors.Is(err, backendErr) {
		t.Fatalf("backend error = %v", err)
	}
	if !body.closed.Load() {
		t.Fatal("discarded response body was not closed")
	}

	transport = &TLSRoundTripper{client: &fakeTLSBackend{do: func(*fhttp.Request) (*fhttp.Response, error) {
		return nil, nil
	}}}
	if _, err := transport.RoundTrip(request); err == nil || !strings.Contains(err.Error(), "nil response") {
		t.Fatalf("nil response error = %v", err)
	}

	transport = &TLSRoundTripper{client: &fakeTLSBackend{do: func(converted *fhttp.Request) (*fhttp.Response, error) {
		return &fhttp.Response{
			Status:     "204 No Content",
			StatusCode: http.StatusNoContent,
			Proto:      "HTTP/1.1",
			ProtoMajor: 1,
			ProtoMinor: 1,
			Request:    converted,
		}, nil
	}}}
	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.Body != http.NoBody {
		t.Fatalf("nil backend body converted to %#v, want http.NoBody", response.Body)
	}

	if _, err := ((*TLSRoundTripper)(nil)).RoundTrip(request); err == nil || !strings.Contains(err.Error(), "not initialized") {
		t.Fatalf("nil transport error = %v", err)
	}
	if _, err := (&TLSRoundTripper{}).RoundTrip(request); err == nil || !strings.Contains(err.Error(), "not initialized") {
		t.Fatalf("uninitialized transport error = %v", err)
	}
	if _, err := transport.RoundTrip(nil); err == nil || !strings.Contains(err.Error(), "nil request") {
		t.Fatalf("nil request error = %v", err)
	}
	if _, err := transport.RoundTrip(&http.Request{}); err == nil || !strings.Contains(err.Error(), "nil URL") {
		t.Fatalf("nil URL error = %v", err)
	}
}

func TestTLSRoundTripperCloseIdleConnections(t *testing.T) {
	t.Parallel()

	backend := &fakeTLSBackend{}
	transport := &TLSRoundTripper{client: backend}
	client := &http.Client{Transport: transport}
	client.CloseIdleConnections()
	transport.CloseIdleConnections()
	if got := backend.closeCalls.Load(); got != 2 {
		t.Fatalf("CloseIdleConnections calls = %d, want 2", got)
	}

	((*TLSRoundTripper)(nil)).CloseIdleConnections()
	(&TLSRoundTripper{}).CloseIdleConnections()
}

func TestNewTLSBackendDisablesInternalRedirects(t *testing.T) {
	t.Parallel()

	profileName, profile, err := resolveTLSProfile("chrome_146")
	if err != nil {
		t.Fatal(err)
	}
	backend, err := newTLSBackend(tlsClientConfig{
		ProfileName:             profileName,
		Profile:                 profile,
		Timeout:                 time.Second,
		TimeoutMilliseconds:     1000,
		FollowRedirects:         false,
		RandomExtensionOrdering: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer backend.CloseIdleConnections()

	redirects, ok := backend.(interface{ GetFollowRedirect() bool })
	if !ok {
		t.Fatal("tls-client backend does not expose redirect configuration")
	}
	if redirects.GetFollowRedirect() {
		t.Fatal("tls-client backend follows redirects internally")
	}
}

func TestDeterministicFHTTPHeaderOrder(t *testing.T) {
	t.Parallel()

	header := http.Header{
		"x-Beta":  {"two"},
		"X-alpha": {"one"},
		"Zed":     {"three"},
		"a-Last":  {"four"},
	}
	want := []string{"a-last", "x-alpha", "x-beta", "zed"}
	for range 100 {
		converted := toFHTTPHeader(header)
		if !reflect.DeepEqual(converted[fhttp.HeaderOrderKey], want) {
			t.Fatalf("header order = %#v, want %#v", converted[fhttp.HeaderOrderKey], want)
		}
	}
}

func TestCloneURLCopiesUserInfo(t *testing.T) {
	t.Parallel()

	original, err := url.Parse("https://user:pass@example.test/path")
	if err != nil {
		t.Fatal(err)
	}
	clone := cloneURL(original)
	if clone == original || clone.User == original.User || clone.String() != original.String() {
		t.Fatalf("clone = %#v, original = %#v", clone, original)
	}

	clone.Path = "/changed"
	if original.Path != "/path" {
		t.Fatal("mutating URL clone changed original")
	}
}

func TestRequestBodyIdentity(t *testing.T) {
	t.Parallel()

	body := io.NopCloser(bytes.NewBufferString("body"))
	request, err := http.NewRequest(http.MethodPost, "https://example.test", body)
	if err != nil {
		t.Fatal(err)
	}
	converted := toFHTTPRequest(request)
	if converted.Body != body {
		t.Fatal("request body identity was not preserved")
	}
}

func TestFHTTPHeaderOrderPrefersBrowserNavigationSequence(t *testing.T) {
	t.Parallel()

	header := http.Header{
		"Accept-Language":            {"en-US,en;q=0.9"},
		"Cookie":                     {"session=fake"},
		"Sec-Fetch-Dest":             {"document"},
		"Accept":                     {"text/html"},
		"User-Agent":                 {"Mozilla/5.0 Test"},
		"Sec-Ch-Ua":                  {`"Chromium";v="123"`},
		"Sec-Ch-Ua-Mobile":           {"?0"},
		"Sec-Ch-Ua-Platform":         {`"macOS"`},
		"Upgrade-Insecure-Requests":  {"1"},
		"Sec-Fetch-Site":             {"same-origin"},
		"Sec-Fetch-Mode":             {"navigate"},
		"Sec-Fetch-User":             {"?1"},
		"Priority":                   {"u=0, i"},
		"X-Unrecognized-Late-Header": {"value"},
	}
	converted := toFHTTPHeader(header)
	want := []string{
		"sec-ch-ua",
		"sec-ch-ua-mobile",
		"sec-ch-ua-platform",
		"upgrade-insecure-requests",
		"user-agent",
		"accept",
		"sec-fetch-site",
		"sec-fetch-mode",
		"sec-fetch-user",
		"sec-fetch-dest",
		"accept-language",
		"priority",
		"cookie",
		"x-unrecognized-late-header",
	}
	if got := converted[fhttp.HeaderOrderKey]; !reflect.DeepEqual(got, want) {
		t.Fatalf("header order = %#v, want %#v", got, want)
	}
}
