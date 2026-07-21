package zillow

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type rewriteTransport struct {
	target *url.URL
	base   http.RoundTripper
}

func (transport *rewriteTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	clone := request.Clone(request.Context())
	clone.URL = cloneURL(request.URL)
	clone.URL.Scheme = transport.target.Scheme
	clone.URL.Host = transport.target.Host
	clone.Host = request.URL.Host

	response, err := transport.base.RoundTrip(clone)
	if response != nil {
		response.Request = request
	}
	return response, err
}

func cloneURL(input *url.URL) *url.URL {
	clone := *input
	return &clone
}

func newLocalZillowClient(t *testing.T, handler http.Handler, options ...ClientOption) (*Client, *httptest.Server) {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)

	target, err := url.Parse(server.URL)
	if err != nil {
		t.Fatalf("url.Parse(server.URL) error = %v", err)
	}
	serverClient := server.Client()
	transport := serverClient.Transport
	if tlsTransport, ok := transport.(*http.Transport); ok {
		clone := tlsTransport.Clone()
		clone.Proxy = nil
		clone.TLSClientConfig = cloneTLSConfig(clone.TLSClientConfig)
		transport = clone
	}
	httpClient := &http.Client{
		Transport: &rewriteTransport{target: target, base: transport},
	}
	allOptions := append([]ClientOption{WithHTTPClient(httpClient)}, options...)
	client, err := NewClient("test-version", allOptions...)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	return client, server
}

func cloneTLSConfig(input *tls.Config) *tls.Config {
	if input == nil {
		return &tls.Config{MinVersion: tls.VersionTLS12}
	}
	clone := input.Clone()
	if clone.MinVersion == 0 {
		clone.MinVersion = tls.VersionTLS12
	}
	return clone
}

func writeJSON(t *testing.T, writer http.ResponseWriter, status int, body string) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	if _, err := io.WriteString(writer, body); err != nil {
		t.Errorf("write response: %v", err)
	}
}

func TestSearchBuildsPutRequestAndNormalizesResponse(t *testing.T) {
	t.Parallel()

	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPut {
			t.Errorf("method = %q, want PUT", request.Method)
		}
		if request.URL.Path != searchEndpointPath {
			t.Errorf("path = %q, want %q", request.URL.Path, searchEndpointPath)
		}
		if request.UserAgent() != "gozillo/test-version" {
			t.Errorf("User-Agent = %q, want gozillo/test-version", request.UserAgent())
		}
		if request.Referer() != "https://www.zillow.com/homes/Seattle,-WA_rb/" {
			t.Errorf("Referer = %q", request.Referer())
		}
		if got := request.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", got)
		}
		if got := request.Header.Get("Origin"); got != "https://www.zillow.com" {
			t.Errorf("Origin = %q, want https://www.zillow.com", got)
		}

		decoder := json.NewDecoder(request.Body)
		decoder.UseNumber()
		var payload map[string]any
		if err := decoder.Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if debug, ok := payload["isDebugRequest"].(bool); !ok || debug {
			t.Errorf("isDebugRequest = %#v, want false", payload["isDebugRequest"])
		}
		requestID, ok := int64FromAny(payload["requestId"])
		if !ok || requestID != 77 {
			t.Errorf("requestId = %#v, want 77", payload["requestId"])
		}
		state, ok := payload["searchQueryState"].(map[string]any)
		if !ok {
			t.Fatalf("searchQueryState = %#v, want object", payload["searchQueryState"])
		}
		assertPathNumber(t, state, 2, "pagination", "currentPage")
		assertPathNumber(t, state, 300000, "filterState", "price", "min")
		assertPathNumber(t, state, 850000, "filterState", "price", "max")
		assertPathNumber(t, state, 3, "filterState", "beds", "min")
		assertPathNumber(t, state, 2.5, "filterState", "baths", "min")
		assertPathString(t, state, "pricea", "filterState", "sortSelection", "value")

		writeJSON(t, writer, http.StatusOK, `{
			"cat1": {
				"searchResults": {
					"listResults": [{
						"zpid": 12345,
						"detailUrl": "/homedetails/example/12345_zpid/",
						"address": "123 Main St, Seattle, WA 98101",
						"unformattedPrice": 1200000,
						"price": "$1.2M",
						"beds": 3,
						"baths": 2.5,
						"area": 1800,
						"homeType": "SINGLE_FAMILY",
						"statusType": "FOR_SALE",
						"imgSrc": "https://photos.example/listing.jpg",
						"latLong": {"latitude": 47.61, "longitude": -122.33},
						"availabilityDate": "2026-08-01",
						"hdpData": {"homeInfo": {"daysOnZillow": 2}}
					}],
					"resultsHash": "stable-hash",
					"relaxedResults": true
				},
				"searchList": {"totalResultCount": 42}
			}
		}`)
	})

	client, _ := newLocalZillowClient(t, handler, WithRequestIDGenerator(func() uint64 { return 77 }))
	profile := validTestProfile()
	result, err := client.SearchWithOptions(context.Background(), profile, SearchOptions{
		Filters: SearchFilters{
			Page:     2,
			MinPrice: 300000,
			MaxPrice: 850000,
			MinBeds:  3,
			MinBaths: 2.5,
			Sort:     "pricea",
		},
		IncludeRaw: true,
	})
	if err != nil {
		t.Fatalf("SearchWithOptions() error = %v", err)
	}

	if len(result.Listings) != 1 {
		t.Fatalf("len(Listings) = %d, want 1", len(result.Listings))
	}
	listing := result.Listings[0]
	if listing.ID != "12345" || listing.URL != "https://www.zillow.com/homedetails/example/12345_zpid/" {
		t.Fatalf("listing identity = (%q, %q)", listing.ID, listing.URL)
	}
	if listing.Price == nil || *listing.Price != 1200000 {
		t.Fatalf("listing Price = %v, want 1200000", listing.Price)
	}
	if listing.Bedrooms == nil || *listing.Bedrooms != 3 || listing.Bathrooms == nil || *listing.Bathrooms != 2.5 {
		t.Fatalf("listing beds/baths = (%v, %v)", listing.Bedrooms, listing.Bathrooms)
	}
	if listing.LivingArea == nil || *listing.LivingArea != 1800 {
		t.Fatalf("listing LivingArea = %v, want 1800", listing.LivingArea)
	}
	if listing.Coordinates.Latitude == nil || *listing.Coordinates.Latitude != 47.61 {
		t.Fatalf("listing latitude = %v", listing.Coordinates.Latitude)
	}
	if listing.DaysOnZillow == nil || *listing.DaysOnZillow != 2 || listing.Availability != "2026-08-01" {
		t.Fatalf("listing freshness/availability = (%v, %q)", listing.DaysOnZillow, listing.Availability)
	}
	if result.Metadata.RequestID != 77 || result.Metadata.CurrentPage != 2 || result.Metadata.Returned != 1 || result.Metadata.TotalResults != 42 {
		t.Fatalf("metadata = %+v", result.Metadata)
	}
	if result.Metadata.ResultsHash != "stable-hash" || !result.Metadata.RelaxedResults {
		t.Fatalf("metadata extras = %+v", result.Metadata)
	}
	if len(result.Raw) == 0 {
		t.Fatal("SearchWithOptions() Raw is empty, want original JSON")
	}

	assertPathNumber(t, profile.SearchQueryState, 1, "pagination", "currentPage")
	if _, ok := lookupPath(profile.SearchQueryState, "filterState", "price"); ok {
		t.Fatal("SearchWithOptions() mutated profile")
	}
}

func TestClientPersistsCookiesInJar(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		call := calls.Add(1)
		cookie, err := request.Cookie("zillow_session")
		if call == 1 {
			if err == nil {
				t.Errorf("first request unexpectedly had cookie %q", cookie.Value)
			}
			http.SetCookie(writer, &http.Cookie{Name: "zillow_session", Value: "test-only", Path: "/", Secure: true})
		} else if err != nil || cookie.Value != "test-only" {
			t.Errorf("second request cookie = %#v, %v", cookie, err)
		}
		writeJSON(t, writer, http.StatusOK, `{"cat1":{"searchResults":{"listResults":[]}}}`)
	})

	client, _ := newLocalZillowClient(t, handler)
	for range 2 {
		if _, err := client.Search(context.Background(), validTestProfile(), SearchFilters{}); err != nil {
			t.Fatalf("Search() error = %v", err)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("request count = %d, want 2", calls.Load())
	}
}

func TestDetectChallengeRequiresChallengeShapedResponse(t *testing.T) {
	t.Parallel()

	validProperty := []byte(`<html><body><script id="__NEXT_DATA__" type="application/json">{"description":"Includes robot check and px-captcha as ordinary text"}</script></body></html>`)
	if reason := detectChallenge(responseHTML, http.StatusOK, "text/html", validProperty); reason != "" {
		t.Fatalf("valid property page classified as challenge: %q", reason)
	}
	validJSON := []byte(`{"cat1":{"searchResults":{"listResults":[]}},"message":"robot check"}`)
	if reason := detectChallenge(responseJSON, http.StatusOK, "application/json", validJSON); reason != "" {
		t.Fatalf("valid JSON classified as challenge: %q", reason)
	}
	challengeHTML := []byte(`<html><body>Verify you are human</body></html>`)
	if reason := detectChallenge(responseHTML, http.StatusOK, "text/html", challengeHTML); reason == "" {
		t.Fatal("challenge-shaped HTML was not classified")
	}
}

func TestSearchClassifiesTerminalResponsesWithoutRetry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		status     int
		content    string
		body       string
		header     http.Header
		wantIs     error
		wantStatus int
		check      func(*testing.T, error)
	}{
		{
			name:    "perimeterx header",
			status:  http.StatusForbidden,
			content: "application/json",
			body:    `{}`,
			header:  http.Header{"X-Px-Blocked": []string{"1"}},
			wantIs:  ErrChallenge,
		},
		{
			name:    "html at json endpoint",
			status:  http.StatusOK,
			content: "text/html",
			body:    `<!doctype html><html><body>temporary block</body></html>`,
			wantIs:  ErrChallenge,
		},
		{
			name:    "captcha marker",
			status:  http.StatusOK,
			content: "application/json",
			body:    `<div id="px-captcha">Press & Hold</div>`,
			wantIs:  ErrChallenge,
		},
		{
			name:    "rate limit",
			status:  http.StatusTooManyRequests,
			content: "application/json",
			body:    `{}`,
			header:  http.Header{"Retry-After": []string{"7"}},
			wantIs:  ErrRateLimited,
			check: func(t *testing.T, err error) {
				var rateLimit *RateLimitError
				if !errors.As(err, &rateLimit) || rateLimit.RetryAfter != 7*time.Second {
					t.Fatalf("error = %#v, want RateLimitError with 7s", err)
				}
			},
		},
		{name: "bad request", status: http.StatusBadRequest, content: "application/json", body: `{}`, wantStatus: http.StatusBadRequest},
		{name: "unauthorized", status: http.StatusUnauthorized, content: "application/json", body: `{}`, wantStatus: http.StatusUnauthorized},
		{name: "forbidden json", status: http.StatusForbidden, content: "application/json", body: `{}`, wantStatus: http.StatusForbidden},
		{name: "not found", status: http.StatusNotFound, content: "application/json", body: `{}`, wantStatus: http.StatusNotFound},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				calls.Add(1)
				for key, values := range test.header {
					for _, value := range values {
						writer.Header().Add(key, value)
					}
				}
				writer.Header().Set("Content-Type", test.content)
				writer.WriteHeader(test.status)
				_, _ = io.WriteString(writer, test.body)
			})
			client, _ := newLocalZillowClient(t, handler)
			_, err := client.Search(context.Background(), validTestProfile(), SearchFilters{})
			if err == nil {
				t.Fatal("Search() error = nil, want classified error")
			}
			if test.wantIs != nil && !errors.Is(err, test.wantIs) {
				t.Fatalf("Search() error = %v, want errors.Is(%v)", err, test.wantIs)
			}
			if test.wantStatus != 0 {
				var statusError *HTTPStatusError
				if !errors.As(err, &statusError) || statusError.StatusCode != test.wantStatus {
					t.Fatalf("Search() error = %#v, want HTTPStatusError %d", err, test.wantStatus)
				}
			}
			if test.check != nil {
				test.check(t, err)
			}
			if calls.Load() != 1 {
				t.Fatalf("request count = %d, want exactly 1", calls.Load())
			}
		})
	}
}

func TestSearchDetectsSchemaDriftAndResponseLimit(t *testing.T) {
	t.Parallel()

	t.Run("missing listResults", func(t *testing.T) {
		t.Parallel()
		client, _ := newLocalZillowClient(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			writeJSON(t, writer, http.StatusOK, `{}`)
		}))
		_, err := client.Search(context.Background(), validTestProfile(), SearchFilters{})
		if !errors.Is(err, ErrSchemaDrift) {
			t.Fatalf("Search() error = %v, want schema drift", err)
		}
	})

	t.Run("malformed listing", func(t *testing.T) {
		t.Parallel()
		client, _ := newLocalZillowClient(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			writeJSON(t, writer, http.StatusOK, `{"cat1":{"searchResults":{"listResults":["not-an-object"]}}}`)
		}))
		_, err := client.Search(context.Background(), validTestProfile(), SearchFilters{})
		if !errors.Is(err, ErrSchemaDrift) {
			t.Fatalf("Search() error = %v, want schema drift", err)
		}
	})

	t.Run("unrecognized listing", func(t *testing.T) {
		t.Parallel()
		client, _ := newLocalZillowClient(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			writeJSON(t, writer, http.StatusOK, `{"cat1":{"searchResults":{"listResults":[{"trackingToken":"opaque"}]}}}`)
		}))
		_, err := client.Search(context.Background(), validTestProfile(), SearchFilters{})
		if !errors.Is(err, ErrSchemaDrift) {
			t.Fatalf("Search() error = %v, want schema drift", err)
		}
	})

	t.Run("incompatible listing field", func(t *testing.T) {
		t.Parallel()
		client, _ := newLocalZillowClient(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			writeJSON(t, writer, http.StatusOK, `{"cat1":{"searchResults":{"listResults":[{"zpid":123,"beds":{"value":3}}]}}}`)
		}))
		_, err := client.Search(context.Background(), validTestProfile(), SearchFilters{})
		if !errors.Is(err, ErrSchemaDrift) {
			t.Fatalf("Search() error = %v, want schema drift", err)
		}
	})

	t.Run("response too large", func(t *testing.T) {
		t.Parallel()
		client, _ := newLocalZillowClient(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			writer.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(writer, strings.Repeat("x", 65))
		}), WithMaxResponseBytes(64))
		_, err := client.Search(context.Background(), validTestProfile(), SearchFilters{})
		if !errors.Is(err, ErrResponseTooLarge) {
			t.Fatalf("Search() error = %v, want response-too-large", err)
		}
	})
}

func TestDecodeSearchResponseRequiresCoreListingIdentity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		listing string
		wantErr bool
	}{
		{name: "numeric id", listing: `{"zpid":123}`},
		{name: "address string", listing: `{"address":"123 Main St"}`},
		{name: "address object", listing: `{"address":{"streetAddress":"123 Main St","city":"Seattle"}}`},
		{name: "detail URL", listing: `{"detailUrl":"/homedetails/example/123_zpid/"}`},
		{name: "recognized identity with unknown extension", listing: `{"zpid":"123","newField":{"shape":"ignored"}}`},
		{name: "null", listing: `null`, wantErr: true},
		{name: "empty object", listing: `{}`, wantErr: true},
		{name: "unknown fields only", listing: `{"trackingToken":"opaque"}`, wantErr: true},
		{name: "blank identities", listing: `{"zpid":" ","detailUrl":" ","address":" "}`, wantErr: true},
		{name: "invalid id type", listing: `{"zpid":{"value":123}}`, wantErr: true},
		{name: "invalid address type", listing: `{"zpid":123,"address":["123 Main St"]}`, wantErr: true},
		{name: "invalid numeric field", listing: `{"zpid":123,"beds":"three"}`, wantErr: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			body := `{"cat1":{"searchResults":{"listResults":[` + test.listing + `]}}}`
			result, err := decodeSearchResponse([]byte(body), 1, map[string]any{})
			if test.wantErr {
				if !errors.Is(err, ErrSchemaDrift) {
					t.Fatalf("decodeSearchResponse() error = %v, want schema drift", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("decodeSearchResponse() error = %v", err)
			}
			if len(result.Listings) != 1 {
				t.Fatalf("len(Listings) = %d, want 1", len(result.Listings))
			}
		})
	}
}

func TestDecodeSearchResponseRejectsNonArrayListResults(t *testing.T) {
	t.Parallel()

	_, err := decodeSearchResponse(
		[]byte(`{"cat1":{"searchResults":{"listResults":{"zpid":123}}}}`),
		1,
		map[string]any{},
	)
	if !errors.Is(err, ErrSchemaDrift) {
		t.Fatalf("decodeSearchResponse() error = %v, want schema drift", err)
	}
}

func TestInt64FromAnyRejectsFractionalAndOutOfRangeValues(t *testing.T) {
	t.Parallel()

	tests := []any{
		json.Number("1.9"),
		json.Number("9223372036854775808"),
		json.Number("-9223372036854775809"),
		float64(1.9),
		math.Inf(1),
	}
	for _, value := range tests {
		if got, ok := int64FromAny(value); ok {
			t.Fatalf("int64FromAny(%v) = %d, true; want rejection", value, got)
		}
	}

	for value, want := range map[string]int64{"1e3": 1000, "-9223372036854775808": math.MinInt64} {
		got, ok := int64FromAny(json.Number(value))
		if !ok || got != want {
			t.Fatalf("int64FromAny(%q) = (%d, %t), want (%d, true)", value, got, ok, want)
		}
	}
}

func TestParseMoneyUsesOneNumericTokenAndAdjacentSuffix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		input  string
		want   int64
		wantOK bool
	}{
		{name: "monthly price does not become millions", input: "$2,500/mo", want: 2500, wantOK: true},
		{name: "minus before currency", input: "-$450K", wantOK: false},
		{name: "minus after currency", input: "$-450K", wantOK: false},
		{name: "unicode minus before currency", input: "−$450K", wantOK: false},
		{name: "unicode minus after currency", input: "$−450K", wantOK: false},
		{name: "minus without currency", input: "-450K", wantOK: false},
		{name: "adjacent thousands suffix", input: "$850K", want: 850000, wantOK: true},
		{name: "adjacent millions suffix", input: "$1.2M", want: 1200000, wantOK: true},
		{name: "adjacent billions suffix", input: "$1.1B", want: 1100000000, wantOK: true},
		{name: "separated suffix is ordinary text", input: "$1 million", want: 1, wantOK: true},
		{name: "suffix must not be prefix of a word", input: "$2motel", want: 2, wantOK: true},
		{name: "trailing punctuation is outside token", input: "$2,500, monthly", want: 2500, wantOK: true},
		{name: "range has two numeric tokens", input: "$500K-$600K", wantOK: false},
		{name: "letters do not join numeric tokens", input: "$12abc34", wantOK: false},
		{name: "malformed grouping", input: "$1,2,3", wantOK: false},
		{name: "no numeric token", input: "contact for price", wantOK: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, ok := parseMoney(test.input)
			if ok != test.wantOK || got != test.want {
				t.Fatalf("parseMoney(%q) = (%d, %t), want (%d, %t)", test.input, got, ok, test.want, test.wantOK)
			}
		})
	}
}

func TestClientRejectsNonZillowURLBeforeTransport(t *testing.T) {
	t.Parallel()

	var calls atomic.Int32
	client, err := NewClient("test", WithHTTPClient(&http.Client{Transport: roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, fmt.Errorf("transport should not be called")
	})}))
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	_, err = client.FetchProperty(context.Background(), "https://www.zillow.com.evil.example/homedetails/1")
	if !errors.Is(err, ErrHostNotAllowed) {
		t.Fatalf("FetchProperty() error = %v, want host-not-allowed", err)
	}
	if calls.Load() != 0 {
		t.Fatalf("transport calls = %d, want 0", calls.Load())
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (function roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestClientUsesExplicitCapturedUserAgent(t *testing.T) {
	t.Parallel()

	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if got := request.UserAgent(); got != "Mozilla/5.0 Captured Browser" {
			t.Errorf("User-Agent = %q", got)
		}
		writeJSON(t, writer, http.StatusOK, `{"cat1":{"searchResults":{"listResults":[]}}}`)
	})
	client, _ := newLocalZillowClient(t, handler, WithUserAgent("Mozilla/5.0 Captured Browser"))
	if _, err := client.Search(context.Background(), validTestProfile(), SearchFilters{}); err != nil {
		t.Fatal(err)
	}
}

func TestClientSendsInitialSessionCookies(t *testing.T) {
	t.Parallel()

	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		cookie, err := request.Cookie("zgsession")
		if err != nil || cookie.Value != "fake" {
			t.Errorf("session cookie = %#v, %v", cookie, err)
		}
		writeJSON(t, writer, http.StatusOK, `{"cat1":{"searchResults":{"listResults":[]}}}`)
	})
	client, _ := newLocalZillowClient(t, handler, WithInitialCookies([]*http.Cookie{{
		Name: "zgsession", Value: "fake", Domain: ".zillow.com", Path: "/", Secure: true,
	}}))
	if _, err := client.Search(context.Background(), validTestProfile(), SearchFilters{}); err != nil {
		t.Fatal(err)
	}
}

func TestClientAppliesAllowlistedBrowserHeadersOnlyToHTML(t *testing.T) {
	t.Parallel()

	var requests []*http.Request
	transport := roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		requests = append(requests, request.Clone(request.Context()))
		contentType := "application/json"
		body := `{}`
		if strings.Contains(request.Header.Get("Accept"), "text/html") {
			contentType = "text/html"
			body = `<html><script id="__NEXT_DATA__" type="application/json">{}</script></html>`
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Header:     http.Header{"Content-Type": []string{contentType}},
			Body:       io.NopCloser(strings.NewReader(body)),
			Request:    request,
		}, nil
	})
	browserHeaders := http.Header{
		"Accept":                    {"text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8"},
		"Accept-Language":           {"en-US,en;q=0.9"},
		"Sec-Ch-Ua":                 {`"Chromium";v="123", "Microsoft Edge";v="123"`},
		"Sec-Ch-Ua-Mobile":          {"?0"},
		"Sec-Ch-Ua-Platform":        {`"macOS"`},
		"Sec-Fetch-Dest":            {"document"},
		"Sec-Fetch-Mode":            {"navigate"},
		"Sec-Fetch-Site":            {"same-origin"},
		"Sec-Fetch-User":            {"?1"},
		"Upgrade-Insecure-Requests": {"1"},
	}
	client, err := NewClient(
		"test",
		WithHTTPClient(&http.Client{Transport: transport}),
		WithUserAgent("Mozilla/5.0 Explicit Browser"),
		WithBrowserHeaders(browserHeaders),
	)
	if err != nil {
		t.Fatal(err)
	}

	htmlURL, _ := url.Parse("https://www.zillow.com/example/rentals/")
	if _, _, err := client.execute(context.Background(), requestSpec{
		operation: "HTML test",
		method:    http.MethodGet,
		url:       htmlURL,
		kind:      responseHTML,
	}); err != nil {
		t.Fatal(err)
	}
	jsonURL, _ := url.Parse("https://www.zillow.com/async-create-search-page-state")
	if _, _, err := client.execute(context.Background(), requestSpec{
		operation: "JSON test",
		method:    http.MethodPut,
		url:       jsonURL,
		body:      []byte(`{}`),
		kind:      responseJSON,
	}); err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 {
		t.Fatalf("transport requests = %d, want 2", len(requests))
	}

	htmlRequest := requests[0]
	if got := htmlRequest.Header.Get("Sec-Ch-Ua"); got == "" {
		t.Fatal("HTML request omitted Sec-CH-UA")
	}
	if got := htmlRequest.Header.Get("Accept-Language"); got != "en-US,en;q=0.9" {
		t.Fatalf("HTML Accept-Language = %q", got)
	}
	if got := htmlRequest.Header.Get("Sec-Fetch-Dest"); got != "document" {
		t.Fatalf("HTML Sec-Fetch-Dest = %q", got)
	}
	if got := htmlRequest.UserAgent(); got != "Mozilla/5.0 Explicit Browser" {
		t.Fatalf("HTML User-Agent = %q", got)
	}

	jsonRequest := requests[1]
	if got := jsonRequest.Header.Get("Sec-Ch-Ua"); got != "" {
		t.Fatalf("JSON Sec-CH-UA = %q, want empty", got)
	}
	if got := jsonRequest.Header.Get("Sec-Fetch-Dest"); got != "" {
		t.Fatalf("JSON Sec-Fetch-Dest = %q, want empty", got)
	}
	if got := jsonRequest.Header.Get("Accept"); got != "application/json" {
		t.Fatalf("JSON Accept = %q", got)
	}
}

func TestWithBrowserHeadersRejectsCredentialAndRoutingHeaders(t *testing.T) {
	t.Parallel()

	for _, name := range []string{
		"Authorization", "Cookie", "Host", "Origin", "Referer", "User-Agent", "X-CSRF-Token",
	} {
		name := name
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := NewClient("test", WithBrowserHeaders(http.Header{name: {"not-a-real-secret"}}))
			if err == nil || !strings.Contains(err.Error(), "is not allowed") {
				t.Fatalf("NewClient() error = %v", err)
			}
		})
	}
}

func TestWithBrowserHeadersRejectsInvalidValuesAndClonesInput(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"", "good\r\nbad"} {
		_, err := NewClient("test", WithBrowserHeaders(http.Header{"Accept-Language": {value}}))
		if err == nil || !strings.Contains(err.Error(), "invalid value") {
			t.Fatalf("NewClient() error = %v for value %q", err, value)
		}
	}

	headers := http.Header{"Accept-Language": {"en-US"}}
	client, err := NewClient("test", WithBrowserHeaders(headers))
	if err != nil {
		t.Fatal(err)
	}
	headers.Set("Accept-Language", "mutated")
	if got := client.browserHeaders.Get("Accept-Language"); got != "en-US" {
		t.Fatalf("browser header clone = %q", got)
	}
}
