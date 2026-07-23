package cdphar

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
)

func TestOptionsValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		options Options
		want    string
	}{
		{
			name:    "invalid endpoint",
			options: Options{Endpoint: "not-an-endpoint", URL: "https://example.com", Timeout: time.Second},
			want:    "invalid CDP endpoint",
		},
		{
			name:    "unsupported endpoint scheme",
			options: Options{Endpoint: "file://localhost/tmp/socket", URL: "https://example.com", Timeout: time.Second},
			want:    "endpoint scheme",
		},
		{
			name:    "invalid URL",
			options: Options{Endpoint: DefaultEndpoint, URL: "zillow.com", Timeout: time.Second},
			want:    "invalid URL",
		},
		{
			name:    "unsupported URL scheme",
			options: Options{Endpoint: DefaultEndpoint, URL: "ftp://example.com", Timeout: time.Second},
			want:    "URL scheme",
		},
		{
			name:    "remote endpoint requires opt in",
			options: Options{Endpoint: "wss://192.0.2.10:9222/devtools/browser/browser-id", URL: "https://example.com", Timeout: time.Second},
			want:    "must be loopback",
		},
		{
			name:    "remote HTTP discovery is unsupported",
			options: Options{Endpoint: "http://192.0.2.10:9222", URL: "https://example.com", Timeout: time.Second, AllowRemoteEndpoint: true},
			want:    "remote CDP requires an exact browser WebSocket URL",
		},
		{
			name:    "https discovery is unsupported",
			options: Options{Endpoint: "https://127.0.0.1:9222", URL: "https://example.com", Timeout: time.Second},
			want:    "endpoint scheme",
		},
		{
			name:    "websocket endpoint must identify browser",
			options: Options{Endpoint: "ws://127.0.0.1:9222/", URL: "https://example.com", Timeout: time.Second},
			want:    "exact browser WebSocket",
		},
		{
			name:    "negative wait",
			options: Options{Endpoint: DefaultEndpoint, URL: "https://example.com", Wait: -time.Second, Timeout: time.Second},
			want:    "wait duration",
		},
		{
			name:    "wait exceeds timeout",
			options: Options{Endpoint: DefaultEndpoint, URL: "https://example.com", Wait: time.Second, Timeout: time.Second},
			want:    "shorter than timeout",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if err := test.options.validate(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validate() error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestResolveBrowserWebSocketURLValidatesDiscoveryResult(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		webSocket  string
		redirectTo string
		want       string
		wantErr    string
	}{
		{
			name:      "loopback websocket",
			webSocket: "same-authority",
		},
		{
			name:      "remote websocket",
			webSocket: "ws://192.0.2.10:9222/devtools/browser/browser-id",
			wantErr:   "different authority",
		},
		{
			name:       "remote discovery redirect",
			redirectTo: "http://192.0.2.10:9222/json/version",
			wantErr:    "changed authority",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if test.redirectTo != "" {
					http.Redirect(w, r, test.redirectTo, http.StatusFound)
					return
				}
				webSocketURL := test.webSocket
				if webSocketURL == "same-authority" {
					webSocketURL = "ws" + strings.TrimPrefix(server.URL, "http") + "/devtools/browser/browser-id"
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(w, `{"webSocketDebuggerUrl":%q}`, webSocketURL)
			}))
			defer server.Close()

			options := Options{Endpoint: server.URL, URL: "https://example.com", Timeout: time.Second}
			got, err := resolveBrowserWebSocketURL(context.Background(), options)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("resolve error = %v, want substring %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := test.want
			if test.webSocket == "same-authority" {
				want = "ws" + strings.TrimPrefix(server.URL, "http") + "/devtools/browser/browser-id"
			}
			if got != want {
				t.Fatalf("resolved URL = %q, want %q", got, want)
			}
		})
	}
}

func TestBrowserCreator(t *testing.T) {
	t.Parallel()

	creator := browserCreator("Edg/146.0.3856.8")
	if creator == nil || creator.Name != "Edg" || creator.Version != "146.0.3856.8" {
		t.Fatalf("browserCreator() = %+v", creator)
	}
	if creator := browserCreator(""); creator != nil {
		t.Fatalf("browserCreator(empty) = %+v, want nil", creator)
	}
	if creator := browserCreator(strings.Repeat("x", maxBrowserProductBytes+100)); creator == nil || len(creator.Name) > maxBrowserProductBytes {
		t.Fatalf("browserCreator(long) = %+v", creator)
	}
}

func TestOptionsAllowExplicitRemoteEndpoint(t *testing.T) {
	t.Parallel()

	options := Options{
		Endpoint:            "wss://192.0.2.10:9222/devtools/browser/browser-id",
		URL:                 "https://example.com",
		Timeout:             time.Second,
		AllowRemoteEndpoint: true,
	}
	if err := options.validate(); err != nil {
		t.Fatalf("validate() error = %v", err)
	}
}

func TestEndpointLabelOmitsBrowserWebSocketPathAndQuery(t *testing.T) {
	t.Parallel()

	got := endpointLabel("wss://browser.example/devtools/browser/opaque-id?mode=test")
	if got != "wss://browser.example" {
		t.Fatalf("endpointLabel() = %q", got)
	}
}

func TestRedactErrorRemovesEndpointDetails(t *testing.T) {
	t.Parallel()

	endpoint := "wss://browser.example/devtools/browser/opaque-id?mode=test"
	got := redactError(&url.Error{Op: "Get", URL: endpoint, Err: errors.New("connection refused")}, endpoint)
	if strings.Contains(got.Error(), "opaque-id") || !strings.Contains(got.Error(), "connection refused") {
		t.Fatalf("redacted error = %q", got)
	}
}

func TestWaitForExtraInfoDrainsPromisedEvents(t *testing.T) {
	t.Parallel()

	wallStart := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	monoStart := testMonotonicStart()
	recorder := newRecorder("https://example.test/", wallStart)
	requestID := network.RequestID("drain")
	recorder.handleEvent(&network.EventRequestWillBeSent{
		RequestID: requestID,
		Request:   &network.Request{URL: "https://example.test/", Method: "GET"},
		Timestamp: monotonicPointer(monoStart),
		WallTime:  wallPointer(wallStart),
		Type:      network.ResourceTypeDocument,
	}, false)
	recorder.handleEvent(&network.EventResponseReceived{
		RequestID:    requestID,
		Timestamp:    monotonicPointer(monoStart.Add(time.Millisecond)),
		Type:         network.ResourceTypeDocument,
		HasExtraInfo: true,
		Response:     &network.Response{Status: 200, Protocol: "h2"},
	}, false)

	go func() {
		time.Sleep(20 * time.Millisecond)
		recorder.handleEvent(&network.EventRequestWillBeSentExtraInfo{RequestID: requestID}, false)
		recorder.handleEvent(&network.EventResponseReceivedExtraInfo{RequestID: requestID, StatusCode: 200}, false)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	waitForExtraInfo(ctx, recorder, 500*time.Millisecond)
	if got := recorder.pendingExtraInfoCount(); got != 0 {
		t.Fatalf("pending extra info = %d, want 0", got)
	}
}

func TestClearEventBufferReleasesReferences(t *testing.T) {
	t.Parallel()

	events := []any{&network.EventRequestWillBeSent{}, &network.EventResponseReceived{}}
	clearEventBuffer(events)
	for index, event := range events {
		if event != nil {
			t.Fatalf("events[%d] = %#v, want nil", index, event)
		}
	}
}

func TestFreezeBufferFiltersAndMeasuresRecorderEvents(t *testing.T) {
	t.Parallel()

	if isRecorderEvent(struct{}{}) {
		t.Fatal("isRecorderEvent(struct{}) = true")
	}
	if !isRecorderEvent(&network.EventDataReceived{}) {
		t.Fatal("isRecorderEvent(dataReceived) = false")
	}
	event := &network.EventRequestWillBeSent{
		Request: &network.Request{
			URL:             "https://example.test/",
			Method:          "POST",
			PostDataEntries: []*network.PostDataEntry{{Bytes: strings.Repeat("x", 1024)}},
		},
	}
	if got := freezeEventBytes(event); got < 1024 {
		t.Fatalf("freezeEventBytes() = %d", got)
	}
	redirect := &network.EventRequestWillBeSent{
		Request: &network.Request{URL: "https://example.test/final", Method: "GET"},
		RedirectResponse: &network.Response{
			Status:  302,
			Headers: network.Headers{"X-Large": strings.Repeat("x", 4096)},
		},
	}
	if got := freezeEventBytes(redirect); got < 4096 {
		t.Fatalf("redirect freezeEventBytes() = %d", got)
	}
}
