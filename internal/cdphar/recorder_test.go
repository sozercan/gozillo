package cdphar

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"

	"gozillo/internal/har"
	"gozillo/internal/session"
)

func TestRecorderBuildsHARWithSensitiveRequestData(t *testing.T) {
	t.Parallel()

	wallStart := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	monoStart := testMonotonicStart()
	recorder := newRecorder("https://www.zillow.com/homes/?q=one&q=two", wallStart.Add(-time.Second))
	requestID := network.RequestID("request-1")
	postData := `{"searchQueryState":{"mapBounds":{}},"wants":{"cat1":["listResults"]}}`

	tasks := recorder.handleEvent(&network.EventRequestWillBeSent{
		RequestID: requestID,
		Request: &network.Request{
			URL:             "https://www.zillow.com/async-create-search-page-state",
			Method:          "PUT",
			Headers:         network.Headers{"Accept": "application/json"},
			HasPostData:     true,
			PostDataEntries: []*network.PostDataEntry{{Bytes: base64.StdEncoding.EncodeToString([]byte(postData))}},
		},
		Timestamp: monotonicPointer(monoStart),
		WallTime:  wallPointer(wallStart),
		Type:      network.ResourceTypeXHR,
		Initiator: &network.Initiator{Type: network.InitiatorTypeScript},
	}, false)
	if len(tasks) != 0 {
		t.Fatalf("request tasks = %#v, want none when postDataEntries is present", tasks)
	}

	recorder.handleEvent(&network.EventRequestWillBeSentExtraInfo{
		RequestID: requestID,
		Headers: network.Headers{
			"Accept":       "application/json",
			"Content-Type": "application/json",
			"Cookie":       "zguid=sent; blocked=not-sent",
			"Referer":      "https://www.zillow.com/homes/for_sale/Seattle-WA/",
		},
		AssociatedCookies: []*network.AssociatedCookie{
			{Cookie: &network.Cookie{Name: "zguid", Value: "sent", Domain: ".zillow.com", Path: "/", Secure: true, HTTPOnly: true, SameSite: network.CookieSameSiteLax}},
			{Cookie: &network.Cookie{Name: "blocked", Value: "not-sent", Domain: ".zillow.com", Path: "/"}, BlockedReasons: []network.CookieBlockedReason{network.CookieBlockedReasonUserPreferences}},
		},
	}, false)

	recorder.handleEvent(&network.EventResponseReceivedExtraInfo{
		RequestID:  requestID,
		StatusCode: 200,
		Headers: network.Headers{
			"Content-Type":   "application/json; charset=utf-8",
			"Content-Length": "321",
			"Set-Cookie":     "session=fresh; Path=/; Secure; HttpOnly; SameSite=Lax",
		},
	}, false)
	recorder.handleEvent(&network.EventResponseReceived{
		RequestID:    requestID,
		Timestamp:    monotonicPointer(monoStart.Add(120 * time.Millisecond)),
		Type:         network.ResourceTypeXHR,
		HasExtraInfo: true,
		Response: &network.Response{
			Status:          200,
			StatusText:      "OK",
			MimeType:        "application/json",
			Protocol:        "h2",
			RemoteIPAddress: "203.0.113.10",
			ConnectionID:    7,
			Timing: &network.ResourceTiming{
				RequestTime:         monotonicSeconds(monoStart),
				DNSStart:            1,
				DNSEnd:              3,
				ConnectStart:        3,
				ConnectEnd:          8,
				SslStart:            4,
				SslEnd:              7,
				SendStart:           9,
				SendEnd:             10,
				ReceiveHeadersStart: 119,
				ReceiveHeadersEnd:   120,
			},
		},
	}, false)
	recorder.handleEvent(&network.EventDataReceived{
		RequestID:         requestID,
		Timestamp:         monotonicPointer(monoStart.Add(140 * time.Millisecond)),
		DataLength:        300,
		EncodedDataLength: 200,
	}, false)
	recorder.handleEvent(&network.EventLoadingFinished{
		RequestID:         requestID,
		Timestamp:         monotonicPointer(monoStart.Add(150 * time.Millisecond)),
		EncodedDataLength: 321,
	}, false)
	recorder.handleEvent(&page.EventDomContentEventFired{Timestamp: monotonicPointer(monoStart.Add(130 * time.Millisecond))}, false)
	recorder.handleEvent(&page.EventLoadEventFired{Timestamp: monotonicPointer(monoStart.Add(140 * time.Millisecond))}, false)

	archive, err := recorder.archive(
		"Zillow search",
		har.Creator{Name: "gozillo", Version: "test"},
		&har.Creator{Name: "Edg", Version: "146.0"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(archive.Log.Entries) != 1 {
		t.Fatalf("entry count = %d, want 1", len(archive.Log.Entries))
	}
	if !strings.Contains(archive.Log.Comment, "primary page target") {
		t.Fatalf("log comment = %q", archive.Log.Comment)
	}
	entry := archive.Log.Entries[0]
	if entry.Request.Method != "PUT" || entry.Request.HTTPVersion != "HTTP/2" {
		t.Fatalf("request = %+v", entry.Request)
	}
	if entry.Request.PostData == nil || entry.Request.PostData.Text != postData || entry.Request.PostData.MimeType != "application/json" {
		t.Fatalf("postData = %+v", entry.Request.PostData)
	}
	if got := entry.Request.QueryString; len(got) != 0 {
		t.Fatalf("queryString = %#v, want empty", got)
	}
	if len(entry.Request.Cookies) != 1 || entry.Request.Cookies[0].Name != "zguid" || entry.Request.Cookies[0].Value != "sent" {
		t.Fatalf("request cookies = %+v", entry.Request.Cookies)
	}
	if len(entry.Response.Cookies) != 1 || entry.Response.Cookies[0].Name != "session" {
		t.Fatalf("response cookies = %+v", entry.Response.Cookies)
	}
	if entry.Response.Status != 200 || entry.Response.Content.Size != 300 || entry.Response.BodySize != 200 || entry.ServerIPAddress != "203.0.113.10" || entry.Connection != "7" {
		t.Fatalf("response entry = %+v", entry)
	}
	timingSum := entry.Timings.Send + entry.Timings.Wait + entry.Timings.Receive
	for _, phase := range []*float64{entry.Timings.Blocked, entry.Timings.DNS, entry.Timings.Connect} {
		if phase != nil {
			timingSum += *phase
		}
	}
	if timingSum < entry.Time-0.001 || timingSum > entry.Time+0.001 {
		t.Fatalf("timing sum = %v, entry time = %v", timingSum, entry.Time)
	}
	if entry.ResourceType != "XHR" {
		t.Fatalf("resource type = %q", entry.ResourceType)
	}
	var initiator map[string]any
	if err := json.Unmarshal(entry.Initiator, &initiator); err != nil || initiator["type"] != "script" {
		t.Fatalf("initiator = %s, err = %v", entry.Initiator, err)
	}
	template, err := har.DeriveSearchTemplate(archive)
	if err != nil {
		t.Fatalf("DeriveSearchTemplate() error = %v", err)
	}
	if template.Endpoint != "https://www.zillow.com/async-create-search-page-state" {
		t.Fatalf("derived endpoint = %q", template.Endpoint)
	}
	imported, err := session.ImportHAR(archive)
	if err != nil {
		t.Fatalf("ImportHAR() error = %v", err)
	}
	if len(imported.Cookies) != 1 || imported.Cookies[0].Name != "zguid" {
		t.Fatalf("imported cookies = %+v", imported.Cookies)
	}
}

func TestRecorderDoesNotTrustContentLengthAfterLoadingFailure(t *testing.T) {
	t.Parallel()

	wallStart := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	monoStart := testMonotonicStart()
	recorder := newRecorder("https://example.test/fail", wallStart)
	requestID := network.RequestID("failed")
	recorder.handleEvent(&network.EventRequestWillBeSent{
		RequestID: requestID,
		Request:   &network.Request{URL: "https://example.test/fail", Method: "GET"},
		Timestamp: monotonicPointer(monoStart),
		WallTime:  wallPointer(wallStart),
		Type:      network.ResourceTypeDocument,
	}, false)
	recorder.handleEvent(&network.EventResponseReceived{
		RequestID: requestID,
		Timestamp: monotonicPointer(monoStart.Add(10 * time.Millisecond)),
		Type:      network.ResourceTypeDocument,
		Response: &network.Response{
			Status:   200,
			Protocol: "h2",
			Headers:  network.Headers{"Content-Length": "1000000"},
		},
	}, false)
	recorder.handleEvent(&network.EventLoadingFailed{
		RequestID: requestID,
		Timestamp: monotonicPointer(monoStart.Add(25 * time.Millisecond)),
		Type:      network.ResourceTypeDocument,
		ErrorText: "net::ERR_NAME_NOT_RESOLVED",
	}, false)

	archive, err := recorder.archive("failed", har.Creator{Name: "test", Version: "1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	entry := archive.Log.Entries[0]
	if entry.Response.Status != 200 || entry.Response.BodySize != -1 || entry.Response.Content.Size != 0 {
		t.Fatalf("failed response = %+v", entry.Response)
	}
	if !strings.Contains(entry.Comment, "ERR_NAME_NOT_RESOLVED") {
		t.Fatalf("comment = %q", entry.Comment)
	}
}

func TestRecorderKeepsRedirectsAsSeparateEntries(t *testing.T) {
	t.Parallel()

	wallStart := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	monoStart := testMonotonicStart()
	recorder := newRecorder("https://example.test/start", wallStart)
	requestID := network.RequestID("redirect")

	recorder.handleEvent(&network.EventRequestWillBeSent{
		RequestID: requestID,
		Request:   &network.Request{URL: "https://example.test/start", Method: "GET"},
		Timestamp: monotonicPointer(monoStart),
		WallTime:  wallPointer(wallStart),
		Type:      network.ResourceTypeDocument,
	}, false)
	recorder.handleEvent(&network.EventRequestWillBeSent{
		RequestID: requestID,
		Request:   &network.Request{URL: "https://example.test/final", Method: "GET"},
		Timestamp: monotonicPointer(monoStart.Add(50 * time.Millisecond)),
		WallTime:  wallPointer(wallStart.Add(50 * time.Millisecond)),
		Type:      network.ResourceTypeDocument,
		RedirectResponse: &network.Response{
			Status:            302,
			StatusText:        "Found",
			Protocol:          "http/1.1",
			Headers:           network.Headers{"Location": "https://example.test/final", "Content-Length": "10"},
			EncodedDataLength: 90,
		},
	}, true)
	recorder.handleEvent(&network.EventResponseReceived{
		RequestID: requestID,
		Timestamp: monotonicPointer(monoStart.Add(100 * time.Millisecond)),
		Type:      network.ResourceTypeDocument,
		Response:  &network.Response{Status: 200, StatusText: "OK", Protocol: "h2"},
	}, false)
	recorder.handleEvent(&network.EventLoadingFinished{
		RequestID: requestID,
		Timestamp: monotonicPointer(monoStart.Add(125 * time.Millisecond)),
	}, false)

	archive, err := recorder.archive("final", har.Creator{Name: "test", Version: "1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(archive.Log.Entries) != 2 {
		t.Fatalf("entry count = %d, want 2", len(archive.Log.Entries))
	}
	if first := archive.Log.Entries[0]; first.Response.Status != 302 || first.Response.RedirectURL != "https://example.test/final" || first.Response.BodySize != 10 || !strings.Contains(first.Response.Content.Comment, "redirect response bodies") {
		t.Fatalf("redirect entry = %+v", first)
	}
	if second := archive.Log.Entries[1]; second.Request.URL != "https://example.test/final" || second.Response.Status != 200 {
		t.Fatalf("final entry = %+v", second)
	}
}

func TestRecorderDoesNotShiftExtraInfoAcrossRedirects(t *testing.T) {
	t.Parallel()

	wallStart := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	monoStart := testMonotonicStart()
	recorder := newRecorder("https://example.test/start", wallStart)
	requestID := network.RequestID("redirect-extra")

	recorder.handleEvent(&network.EventRequestWillBeSent{
		RequestID: requestID,
		Request:   &network.Request{URL: "https://example.test/start", Method: "GET"},
		Timestamp: monotonicPointer(monoStart),
		WallTime:  wallPointer(wallStart),
		Type:      network.ResourceTypeDocument,
	}, false)
	recorder.handleEvent(&network.EventRequestWillBeSent{
		RequestID:            requestID,
		Request:              &network.Request{URL: "https://example.test/final", Method: "GET"},
		Timestamp:            monotonicPointer(monoStart.Add(50 * time.Millisecond)),
		WallTime:             wallPointer(wallStart.Add(50 * time.Millisecond)),
		Type:                 network.ResourceTypeDocument,
		RedirectHasExtraInfo: false,
		RedirectResponse:     &network.Response{Status: 302, Protocol: "h2"},
	}, false)
	recorder.handleEvent(&network.EventRequestWillBeSentExtraInfo{
		RequestID: requestID,
		Headers:   network.Headers{"Cookie": "final=sent"},
		AssociatedCookies: []*network.AssociatedCookie{{
			Cookie: &network.Cookie{Name: "final", Value: "sent", Domain: "example.test", Path: "/"},
		}},
	}, false)
	recorder.handleEvent(&network.EventResponseReceivedExtraInfo{
		RequestID:  requestID,
		StatusCode: 200,
		Headers:    network.Headers{"Content-Type": "text/plain"},
	}, false)
	recorder.handleEvent(&network.EventResponseReceived{
		RequestID:    requestID,
		Timestamp:    monotonicPointer(monoStart.Add(100 * time.Millisecond)),
		Type:         network.ResourceTypeDocument,
		HasExtraInfo: true,
		Response:     &network.Response{Status: 200, Protocol: "h2"},
	}, false)
	recorder.handleEvent(&network.EventLoadingFinished{
		RequestID: requestID,
		Timestamp: monotonicPointer(monoStart.Add(120 * time.Millisecond)),
	}, false)

	archive, err := recorder.archive("final", har.Creator{Name: "test", Version: "1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(archive.Log.Entries) != 2 {
		t.Fatalf("entry count = %d, want 2", len(archive.Log.Entries))
	}
	if got := archive.Log.Entries[0].Request.Cookies; len(got) != 0 {
		t.Fatalf("redirect cookies = %+v, want none", got)
	}
	if got := archive.Log.Entries[1].Request.Cookies; len(got) != 1 || got[0].Name != "final" {
		t.Fatalf("final cookies = %+v", got)
	}
}

func TestResponseBodyFetchWaitsForAuthoritativeExtraInfo(t *testing.T) {
	t.Parallel()

	wallStart := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	monoStart := testMonotonicStart()
	recorder := newRecorder("https://example.test/cache", wallStart)
	requestID := network.RequestID("late-extra")
	recorder.handleEvent(&network.EventRequestWillBeSent{
		RequestID: requestID,
		Request:   &network.Request{URL: "https://example.test/cache", Method: "GET"},
		Timestamp: monotonicPointer(monoStart),
		WallTime:  wallPointer(wallStart),
		Type:      network.ResourceTypeFetch,
	}, true)
	recorder.handleEvent(&network.EventResponseReceived{
		RequestID:    requestID,
		Timestamp:    monotonicPointer(monoStart.Add(time.Millisecond)),
		Type:         network.ResourceTypeFetch,
		HasExtraInfo: true,
		Response:     &network.Response{Status: 200, Protocol: "h2"},
	}, true)
	tasks := recorder.handleEvent(&network.EventLoadingFinished{
		RequestID: requestID,
		Timestamp: monotonicPointer(monoStart.Add(2 * time.Millisecond)),
	}, true)
	if len(tasks) != 0 {
		t.Fatalf("tasks before extra info = %#v", tasks)
	}
	recorder.freezeAtMonotonic(monoStart.Add(3 * time.Millisecond))
	tasks = recorder.handleEvent(&network.EventResponseReceivedExtraInfo{
		RequestID:  requestID,
		StatusCode: 304,
	}, true)
	if len(tasks) != 0 {
		t.Fatalf("tasks for authoritative 304 = %#v", tasks)
	}
	archive, err := recorder.archive("cache", har.Creator{Name: "test", Version: "1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	entry := archive.Log.Entries[0]
	if entry.Response.Status != 304 || entry.Response.Content.Comment != "" {
		t.Fatalf("response = %+v", entry.Response)
	}

	ready := newRecorder("https://example.test/body", wallStart)
	ready.handleEvent(&network.EventRequestWillBeSent{RequestID: "ready", Request: &network.Request{URL: "https://example.test/body", Method: "GET"}, Timestamp: monotonicPointer(monoStart), WallTime: wallPointer(wallStart), Type: network.ResourceTypeFetch}, true)
	ready.handleEvent(&network.EventResponseReceived{RequestID: "ready", Timestamp: monotonicPointer(monoStart.Add(time.Millisecond)), Type: network.ResourceTypeFetch, HasExtraInfo: true, Response: &network.Response{Status: 200, Protocol: "h2"}}, true)
	ready.handleEvent(&network.EventLoadingFinished{RequestID: "ready", Timestamp: monotonicPointer(monoStart.Add(2 * time.Millisecond))}, true)
	ready.freezeAtMonotonic(monoStart.Add(3 * time.Millisecond))
	tasks = ready.handleEvent(&network.EventResponseReceivedExtraInfo{RequestID: "ready", StatusCode: 200}, true)
	if len(tasks) != 1 {
		t.Fatalf("frozen late-extra tasks = %#v, want one", tasks)
	}
}

func TestRecorderSchedulesFetchesAndEncodesBinaryBodies(t *testing.T) {
	t.Parallel()

	wallStart := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	monoStart := testMonotonicStart()
	recorder := newRecorder("https://example.test/upload", wallStart)
	requestID := network.RequestID("body")

	tasks := recorder.handleEvent(&network.EventRequestWillBeSent{
		RequestID: requestID,
		Request:   &network.Request{URL: "https://example.test/upload", Method: "POST", HasPostData: true},
		Timestamp: monotonicPointer(monoStart),
		WallTime:  wallPointer(wallStart),
		Type:      network.ResourceTypeFetch,
	}, true)
	if len(tasks) != 0 {
		t.Fatalf("post data tasks = %#v, want none for an incomplete event body", tasks)
	}
	recorder.handleEvent(&network.EventResponseReceived{
		RequestID: requestID,
		Timestamp: monotonicPointer(monoStart.Add(10 * time.Millisecond)),
		Type:      network.ResourceTypeFetch,
		Response:  &network.Response{Status: 200, Protocol: "h2", MimeType: "application/octet-stream"},
	}, true)
	tasks = recorder.handleEvent(&network.EventLoadingFinished{
		RequestID: requestID,
		Timestamp: monotonicPointer(monoStart.Add(20 * time.Millisecond)),
	}, true)
	if len(tasks) != 1 {
		t.Fatalf("response body tasks = %#v", tasks)
	}
	recorder.setResponseBody(tasks[0].entry, []byte("abc"), nil)

	archive, err := recorder.archive("upload", har.Creator{Name: "test", Version: "1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	entry := archive.Log.Entries[0]
	if entry.Request.PostData == nil || entry.Request.PostData.Text != "" || entry.Request.BodySize != -1 || !strings.Contains(entry.Request.PostData.Comment, "not provide a complete") {
		t.Fatalf("postData = %+v, bodySize = %d", entry.Request.PostData, entry.Request.BodySize)
	}
	if entry.Response.Content.Encoding != "base64" || entry.Response.Content.Text != base64.StdEncoding.EncodeToString([]byte("abc")) {
		t.Fatalf("content = %+v", entry.Response.Content)
	}
}

func TestRecorderLeavesAmbiguousInflightExtraInfoUnassigned(t *testing.T) {
	t.Parallel()

	wallStart := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	monoStart := testMonotonicStart()
	recorder := newRecorder("https://example.test/stream", wallStart)
	requestID := network.RequestID("stream")
	recorder.handleEvent(&network.EventRequestWillBeSent{
		RequestID: requestID,
		Request:   &network.Request{URL: "https://example.test/stream", Method: "GET"},
		Timestamp: monotonicPointer(monoStart),
		WallTime:  wallPointer(wallStart),
		Type:      network.ResourceTypeEventSource,
	}, false)
	recorder.handleEvent(&network.EventRequestWillBeSentExtraInfo{
		RequestID: requestID,
		Headers:   network.Headers{"Cookie": "stream=sent"},
		AssociatedCookies: []*network.AssociatedCookie{{
			Cookie: &network.Cookie{Name: "stream", Value: "sent", Domain: "example.test", Path: "/"},
		}},
	}, false)

	recorder.freezeAtMonotonic(monoStart.Add(time.Second))
	recorder.markMissingExtraInfo()
	archive, err := recorder.archive("stream", har.Creator{Name: "test", Version: "1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	entry := archive.Log.Entries[0]
	if len(entry.Request.Cookies) != 0 || !strings.Contains(entry.Comment, "supplemental") {
		t.Fatalf("entry = %+v", entry)
	}
}

func TestTerminalFailureReconcilesSupplementalStatusAndHeaders(t *testing.T) {
	t.Parallel()

	wallStart := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	monoStart := testMonotonicStart()
	recorder := newRecorder("https://example.test/cors", wallStart)
	requestID := network.RequestID("cors")
	recorder.handleEvent(&network.EventRequestWillBeSent{RequestID: requestID, Request: &network.Request{URL: "https://example.test/cors", Method: "GET"}, Timestamp: monotonicPointer(monoStart), WallTime: wallPointer(wallStart), Type: network.ResourceTypeFetch}, false)
	recorder.handleEvent(&network.EventRequestWillBeSentExtraInfo{RequestID: requestID, Headers: network.Headers{"X-Request": "sent"}}, false)
	recorder.handleEvent(&network.EventLoadingFailed{RequestID: requestID, Timestamp: monotonicPointer(monoStart.Add(time.Millisecond)), Type: network.ResourceTypeFetch, ErrorText: "cors"}, false)
	recorder.handleEvent(&network.EventResponseReceivedExtraInfo{RequestID: requestID, StatusCode: 403, Headers: network.Headers{"X-Response": "blocked"}}, false)
	archive, err := recorder.archive("cors", har.Creator{Name: "test", Version: "1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	entry := archive.Log.Entries[0]
	if entry.Response.Status != 403 || firstHeader(entry.Request.Headers, "X-Request") != "sent" || firstHeader(entry.Response.Headers, "X-Response") != "blocked" {
		t.Fatalf("entry = %+v", entry)
	}
}

func TestBufferedPreCutoffCacheEventIsPreserved(t *testing.T) {
	t.Parallel()

	wallStart := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	monoStart := testMonotonicStart()
	recorder := newRecorder("https://example.test/cache", wallStart)
	requestID := network.RequestID("buffered-cache")
	recorder.handleEvent(&network.EventRequestWillBeSent{RequestID: requestID, Request: &network.Request{URL: "https://example.test/cache", Method: "GET"}, Timestamp: monotonicPointer(monoStart), WallTime: wallPointer(wallStart), Type: network.ResourceTypeFetch}, false)
	recorder.handleEvent(&network.EventResponseReceived{RequestID: requestID, Timestamp: monotonicPointer(monoStart.Add(time.Millisecond)), Type: network.ResourceTypeFetch, Response: &network.Response{Status: 200, Protocol: "h2", Headers: network.Headers{"Content-Length": "10"}}}, false)
	recorder.handleEvent(&network.EventLoadingFinished{RequestID: requestID, Timestamp: monotonicPointer(monoStart.Add(2 * time.Millisecond))}, false)
	recorder.freezeAtMonotonic(monoStart.Add(3 * time.Millisecond))
	recorder.handleBufferedEvent(&network.EventRequestServedFromCache{RequestID: requestID}, false)
	archive, err := recorder.archive("cache", har.Creator{Name: "test", Version: "1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	entry := archive.Log.Entries[0]
	if !strings.Contains(entry.Comment, "browser cache") || entry.Response.BodySize != 0 {
		t.Fatalf("entry = %+v", entry)
	}
}

func TestPostCutoffCacheEventDoesNotCorruptCapturedHop(t *testing.T) {
	t.Parallel()

	wallStart := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	monoStart := testMonotonicStart()
	recorder := newRecorder("https://example.test/start", wallStart)
	requestID := network.RequestID("redirect-cache")
	recorder.handleEvent(&network.EventRequestWillBeSent{RequestID: requestID, Request: &network.Request{URL: "https://example.test/start", Method: "GET"}, Timestamp: monotonicPointer(monoStart), WallTime: wallPointer(wallStart), Type: network.ResourceTypeDocument}, false)
	recorder.handleEvent(&network.EventResponseReceived{RequestID: requestID, Timestamp: monotonicPointer(monoStart.Add(time.Millisecond)), Type: network.ResourceTypeDocument, Response: &network.Response{Status: 302, Protocol: "h2", Headers: network.Headers{"Content-Length": "10"}}}, false)
	recorder.freezeAtMonotonic(monoStart.Add(2 * time.Millisecond))
	recorder.handleEvent(&network.EventRequestWillBeSent{RequestID: requestID, Request: &network.Request{URL: "https://example.test/after", Method: "GET"}, Timestamp: monotonicPointer(monoStart.Add(3 * time.Millisecond)), WallTime: wallPointer(wallStart.Add(3 * time.Millisecond)), Type: network.ResourceTypeDocument, RedirectResponse: &network.Response{Status: 302}}, false)
	recorder.handleEvent(&network.EventRequestServedFromCache{RequestID: requestID}, false)
	archive, err := recorder.archive("start", har.Creator{Name: "test", Version: "1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	entry := archive.Log.Entries[0]
	if strings.Contains(entry.Comment, "browser cache") || entry.Response.BodySize == 0 {
		t.Fatalf("entry = %+v", entry)
	}
}

func TestBodylessResponsesIgnoreRepresentationContentLength(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		method string
		status int64
	}{
		{name: "head", method: http.MethodHead, status: http.StatusOK},
		{name: "not modified", method: http.MethodGet, status: http.StatusNotModified},
		{name: "no content", method: http.MethodGet, status: http.StatusNoContent},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			entry := &capturedEntry{
				request:  &network.Request{Method: test.method},
				response: &network.Response{Status: test.status, Protocol: "h2"},
			}
			response := entry.harResponse([]har.NameValue{{Name: "Content-Length", Value: "123"}}, nil)
			if response.BodySize != 0 || response.Content.Size != 0 {
				t.Fatalf("response = %+v", response)
			}
		})
	}
}

func TestUnfinishedEntryUsesLatestObservedEvent(t *testing.T) {
	t.Parallel()

	wallStart := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	monoStart := testMonotonicStart()
	recorder := newRecorder("https://example.test/stream", wallStart)
	requestID := network.RequestID("unfinished-stream")
	recorder.handleEvent(&network.EventRequestWillBeSent{
		RequestID: requestID,
		Request:   &network.Request{URL: "https://example.test/stream", Method: "GET"},
		Timestamp: monotonicPointer(monoStart),
		WallTime:  wallPointer(wallStart),
		Type:      network.ResourceTypeFetch,
	}, false)
	recorder.handleEvent(&network.EventResponseReceived{
		RequestID: requestID,
		Timestamp: monotonicPointer(monoStart.Add(10 * time.Millisecond)),
		Type:      network.ResourceTypeFetch,
		Response:  &network.Response{Status: 200, Protocol: "h2"},
	}, false)
	recorder.handleEvent(&network.EventDataReceived{
		RequestID:         requestID,
		Timestamp:         monotonicPointer(monoStart.Add(75 * time.Millisecond)),
		DataLength:        5,
		EncodedDataLength: 5,
	}, false)

	recorder.freezeAtMonotonic(monoStart.Add(5 * time.Second))
	archive, err := recorder.archive("stream", har.Creator{Name: "test", Version: "1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	entry := archive.Log.Entries[0]
	if entry.Time != 5000 {
		t.Fatalf("entry time = %v, want 5000ms", entry.Time)
	}
	if !strings.Contains(entry.Comment, "still in flight") {
		t.Fatalf("entry comment = %q", entry.Comment)
	}
}

func TestCachedResponseHasZeroTransferredBodySize(t *testing.T) {
	t.Parallel()

	entry := &capturedEntry{
		request:         &network.Request{Method: http.MethodGet},
		response:        &network.Response{Status: http.StatusOK, Protocol: "h2", FromDiskCache: true},
		loadingFinished: true,
	}
	response := entry.harResponse([]har.NameValue{{Name: "Content-Length", Value: "123"}}, nil)
	if response.BodySize != 0 || response.Content.Size != 123 {
		t.Fatalf("response = %+v", response)
	}
}

func TestProxyNegotiationIsIncludedInBlockedTiming(t *testing.T) {
	t.Parallel()

	monoStart := testMonotonicStart()
	entry := &capturedEntry{
		startedMono: monoStart,
		response: &network.Response{Timing: &network.ResourceTiming{
			RequestTime:       monotonicSeconds(monoStart),
			ProxyStart:        0,
			ProxyEnd:          40,
			DNSStart:          -1,
			DNSEnd:            -1,
			ConnectStart:      -1,
			ConnectEnd:        -1,
			SslStart:          -1,
			SslEnd:            -1,
			SendStart:         40,
			SendEnd:           41,
			ReceiveHeadersEnd: 50,
		}},
	}
	timings := entry.harTimings(monoStart.Add(55 * time.Millisecond))
	if timings.Blocked == nil || *timings.Blocked < 39.999 || *timings.Blocked > 40.001 {
		if timings.Blocked == nil {
			t.Fatal("blocked timing = nil")
		}
		t.Fatalf("blocked timing = %v", *timings.Blocked)
	}
}

func TestRecorderEnforcesEntryAndByteRetentionLimits(t *testing.T) {
	t.Parallel()

	wallStart := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	monoStart := testMonotonicStart()
	recorder := newRecorder("https://example.test/", wallStart)
	recorder.maxEntries = 1
	for index := 0; index < 2; index++ {
		recorder.handleEvent(&network.EventRequestWillBeSent{
			RequestID: network.RequestID(fmt.Sprintf("entry-%d", index)),
			Request:   &network.Request{URL: fmt.Sprintf("https://example.test/%d", index), Method: "GET"},
			Timestamp: monotonicPointer(monoStart.Add(time.Duration(index) * time.Millisecond)),
			WallTime:  wallPointer(wallStart.Add(time.Duration(index) * time.Millisecond)),
			Type:      network.ResourceTypeFetch,
		}, false)
	}
	if err := recorder.captureError(); err == nil || !strings.Contains(err.Error(), "retention limit") {
		t.Fatalf("captureError() = %v", err)
	}

	byteLimited := newRecorder("https://example.test/", wallStart)
	byteLimited.maxBytes = 16
	byteLimited.handleEvent(&network.EventRequestWillBeSent{
		RequestID: "bytes",
		Request:   &network.Request{URL: "https://example.test/large", Method: "GET"},
		Timestamp: monotonicPointer(monoStart),
		WallTime:  wallPointer(wallStart),
		Type:      network.ResourceTypeFetch,
	}, false)
	if err := byteLimited.captureError(); err == nil || !strings.Contains(err.Error(), "retained-data limit") {
		t.Fatalf("byte captureError() = %v", err)
	}

	extraLimited := newRecorder("https://example.test/", wallStart)
	extraLimited.maxBytes = 4096
	extraLimited.handleEvent(&network.EventRequestWillBeSent{
		RequestID: "extra",
		Request:   &network.Request{URL: "https://example.test/", Method: "GET"},
		Timestamp: monotonicPointer(monoStart),
		WallTime:  wallPointer(wallStart),
		Type:      network.ResourceTypeDocument,
	}, false)
	extraLimited.handleEvent(&network.EventResponseReceivedExtraInfo{
		RequestID:   "extra",
		StatusCode:  200,
		HeadersText: strings.Repeat("x", 4096),
	}, false)
	if err := extraLimited.captureError(); err == nil || !strings.Contains(err.Error(), "retained-data limit") {
		t.Fatalf("extra-info captureError() = %v", err)
	}

	redirectLimited := newRecorder("https://example.test/", wallStart)
	redirectLimited.maxBytes = 4096
	redirectLimited.handleEvent(&network.EventRequestWillBeSent{
		RequestID: "redirect-limit",
		Request:   &network.Request{URL: "https://example.test/start", Method: "GET"},
		Timestamp: monotonicPointer(monoStart),
		WallTime:  wallPointer(wallStart),
		Type:      network.ResourceTypeDocument,
	}, false)
	redirectLimited.handleEvent(&network.EventRequestWillBeSent{
		RequestID: "redirect-limit",
		Request:   &network.Request{URL: "https://example.test/final", Method: "GET"},
		Timestamp: monotonicPointer(monoStart.Add(time.Millisecond)),
		WallTime:  wallPointer(wallStart.Add(time.Millisecond)),
		Type:      network.ResourceTypeDocument,
		RedirectResponse: &network.Response{
			Status:  302,
			Headers: network.Headers{"X-Large": strings.Repeat("x", 4096)},
		},
	}, false)
	if err := redirectLimited.captureError(); err == nil || !strings.Contains(err.Error(), "retained-data limit") {
		t.Fatalf("redirect captureError() = %v", err)
	}

	bodyLimited := newRecorder("https://example.test/", wallStart)
	bodyLimited.maxBytes = bodyLimited.retainedBytes + 8
	bodyEntry := &capturedEntry{response: &network.Response{MimeType: "application/octet-stream"}}
	bodyLimited.setResponseBody(bodyEntry, []byte("abc"), nil)
	if err := bodyLimited.captureError(); err == nil || !strings.Contains(err.Error(), "retained-data limit") {
		t.Fatalf("body captureError() = %v", err)
	}
}

func TestRecorderRejectsDerivedCollectionAmplification(t *testing.T) {
	t.Parallel()

	wallStart := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	monoStart := testMonotonicStart()
	tests := []struct {
		name    string
		url     string
		headers network.Headers
		want    string
	}{
		{
			name: "query items",
			url:  "https://example.test/?" + strings.Repeat("&", maxQueryItems),
			want: "query items",
		},
		{
			name:    "header values",
			url:     "https://example.test/",
			headers: network.Headers{"X-Many": strings.Repeat("\n", maxHeaderItems)},
			want:    "header values",
		},
		{
			name:    "cookie pairs",
			url:     "https://example.test/",
			headers: network.Headers{"Cookie": strings.Repeat("a=1;", maxHeaderItems)},
			want:    "cookies",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			recorder := newRecorder(test.url, wallStart)
			recorder.handleEvent(&network.EventRequestWillBeSent{
				RequestID: "limit",
				Request:   &network.Request{URL: test.url, Method: "GET", Headers: test.headers},
				Timestamp: monotonicPointer(monoStart),
				WallTime:  wallPointer(wallStart),
				Type:      network.ResourceTypeFetch,
			}, false)
			if err := recorder.captureError(); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("captureError() = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestRecorderFreezeStopsNewNetworkEntriesButAcceptsExtraInfo(t *testing.T) {
	t.Parallel()

	wallStart := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	monoStart := testMonotonicStart()
	recorder := newRecorder("https://example.test/", wallStart)
	recorder.handleEvent(&network.EventRequestWillBeSent{
		RequestID: "before",
		Request:   &network.Request{URL: "https://example.test/before", Method: "GET"},
		Timestamp: monotonicPointer(monoStart),
		WallTime:  wallPointer(wallStart),
		Type:      network.ResourceTypeFetch,
	}, false)
	recorder.handleEvent(&network.EventRequestWillBeSentExtraInfo{
		RequestID: "mixed-orphan",
		Headers:   network.Headers{"X-Before": "request-extra"},
	}, false)
	recorder.freezeAtMonotonic(monoStart.Add(time.Second))
	recorder.handleEvent(&network.EventResponseReceived{
		RequestID:    "before",
		Timestamp:    monotonicPointer(monoStart.Add(500 * time.Millisecond)),
		Type:         network.ResourceTypeFetch,
		HasExtraInfo: true,
		Response:     &network.Response{Status: 200, Protocol: "h2"},
	}, false)
	recorder.handleEvent(&network.EventRequestWillBeSent{
		RequestID: "prequeued",
		Request:   &network.Request{URL: "https://example.test/prequeued", Method: "GET"},
		Timestamp: monotonicPointer(monoStart.Add(750 * time.Millisecond)),
		WallTime:  wallPointer(wallStart.Add(750 * time.Millisecond)),
		Type:      network.ResourceTypeFetch,
	}, false)
	recorder.handleEvent(&network.EventRequestWillBeSentExtraInfo{
		RequestID: "preextra",
		Headers:   network.Headers{"X-Pre": "extra"},
	}, false)
	recorder.handleEvent(&network.EventRequestWillBeSent{
		RequestID: "preextra",
		Request:   &network.Request{URL: "https://example.test/preextra", Method: "GET"},
		Timestamp: monotonicPointer(monoStart.Add(900 * time.Millisecond)),
		WallTime:  wallPointer(wallStart.Add(900 * time.Millisecond)),
		Type:      network.ResourceTypeFetch,
	}, false)
	recorder.handleEvent(&network.EventResponseReceived{
		RequestID:    "preextra",
		Timestamp:    monotonicPointer(monoStart.Add(920 * time.Millisecond)),
		Type:         network.ResourceTypeFetch,
		HasExtraInfo: true,
		Response:     &network.Response{Status: 200, Protocol: "h2"},
	}, false)
	recorder.handleEvent(&network.EventResponseReceivedExtraInfo{
		RequestID:  "mixed-orphan",
		StatusCode: 201,
		Headers:    network.Headers{"X-After": "response-extra"},
	}, false)
	recorder.handleEvent(&network.EventRequestWillBeSent{
		RequestID: "mixed-orphan",
		Request:   &network.Request{URL: "https://example.test/mixed", Method: "GET"},
		Timestamp: monotonicPointer(monoStart.Add(950 * time.Millisecond)),
		WallTime:  wallPointer(wallStart.Add(950 * time.Millisecond)),
		Type:      network.ResourceTypeFetch,
	}, false)
	recorder.handleEvent(&network.EventResponseReceived{
		RequestID:    "mixed-orphan",
		Timestamp:    monotonicPointer(monoStart.Add(975 * time.Millisecond)),
		Type:         network.ResourceTypeFetch,
		HasExtraInfo: true,
		Response:     &network.Response{Status: 201, Protocol: "h2"},
	}, false)
	recorder.handleEvent(&network.EventRequestWillBeSent{
		RequestID: "after",
		Request:   &network.Request{URL: "https://example.test/after", Method: "GET"},
		Timestamp: monotonicPointer(monoStart.Add(2 * time.Second)),
		WallTime:  wallPointer(wallStart.Add(2 * time.Second)),
		Type:      network.ResourceTypeFetch,
	}, false)
	recorder.handleEvent(&network.EventRequestWillBeSentExtraInfo{
		RequestID: "before",
		Headers:   network.Headers{"Cookie": "a=1"},
	}, false)
	recorder.markMissingExtraInfo()
	archive, err := recorder.archive("frozen", har.Creator{Name: "test", Version: "1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(archive.Log.Entries) != 4 || archive.Log.Entries[0].Request.URL != "https://example.test/before" || archive.Log.Entries[1].Request.URL != "https://example.test/prequeued" || archive.Log.Entries[2].Request.URL != "https://example.test/preextra" || archive.Log.Entries[3].Request.URL != "https://example.test/mixed" {
		t.Fatalf("entries = %+v", archive.Log.Entries)
	}
	if firstHeader(archive.Log.Entries[2].Request.Headers, "X-Pre") != "extra" {
		t.Fatalf("preextra headers = %+v", archive.Log.Entries[2].Request.Headers)
	}
	mixed := archive.Log.Entries[3]
	if firstHeader(mixed.Request.Headers, "X-Before") != "request-extra" || firstHeader(mixed.Response.Headers, "X-After") != "response-extra" || mixed.Response.Status != 201 {
		t.Fatalf("mixed entry = %+v", mixed)
	}
	if firstHeader(archive.Log.Entries[0].Request.Headers, "Cookie") != "a=1" {
		t.Fatalf("request headers = %+v", archive.Log.Entries[0].Request.Headers)
	}
}

func TestFrozenOrphanExtraKeepsDrainAlive(t *testing.T) {
	t.Parallel()

	recorder := newRecorder("https://example.test/", time.Now().UTC())
	recorder.freezeAtMonotonic(testMonotonicStart())
	recorder.handleEvent(&network.EventRequestWillBeSentExtraInfo{
		RequestID: "orphan",
		Headers:   network.Headers{"X-Orphan": "1"},
	}, false)
	if !recorder.extraInfoDrainNeeded() {
		t.Fatal("extraInfoDrainNeeded() = false for frozen orphan")
	}
	archive, err := recorder.archive("orphan", har.Creator{Name: "test", Version: "1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(archive.Log.Comment, "no matching base request") {
		t.Fatalf("log comment = %q", archive.Log.Comment)
	}
}

func TestRecorderMarksPromisedExtraInfoMissingAtCutoff(t *testing.T) {
	t.Parallel()

	wallStart := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	monoStart := testMonotonicStart()
	recorder := newRecorder("https://example.test/", wallStart)
	requestID := network.RequestID("missing-extra")
	recorder.handleEvent(&network.EventRequestWillBeSent{
		RequestID: requestID,
		Request:   &network.Request{URL: "https://example.test/", Method: "GET"},
		Timestamp: monotonicPointer(monoStart),
		WallTime:  wallPointer(wallStart),
		Type:      network.ResourceTypeDocument,
	}, false)
	recorder.handleEvent(&network.EventResponseReceived{
		RequestID:    requestID,
		Timestamp:    monotonicPointer(monoStart.Add(10 * time.Millisecond)),
		Type:         network.ResourceTypeDocument,
		HasExtraInfo: true,
		Response:     &network.Response{Status: 200, Protocol: "h2"},
	}, false)
	recorder.handleEvent(&network.EventLoadingFinished{
		RequestID: requestID,
		Timestamp: monotonicPointer(monoStart.Add(20 * time.Millisecond)),
	}, false)
	if got := recorder.pendingExtraInfoCount(); got != 2 {
		t.Fatalf("pending extra info = %d, want 2", got)
	}
	recorder.markMissingExtraInfo()
	archive, err := recorder.archive("missing", har.Creator{Name: "test", Version: "1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(archive.Log.Entries[0].Comment, "supplemental") {
		t.Fatalf("entry comment = %q", archive.Log.Entries[0].Comment)
	}
}

func TestRecorderIgnoresDuplicateSameIDBaseRequest(t *testing.T) {
	t.Parallel()

	wallStart := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	monoStart := testMonotonicStart()
	recorder := newRecorder("https://example.test/", wallStart)
	for _, rawURL := range []string{"https://example.test/first", "https://example.test/duplicate"} {
		recorder.handleEvent(&network.EventRequestWillBeSent{
			RequestID: "same",
			Request:   &network.Request{URL: rawURL, Method: "GET"},
			Timestamp: monotonicPointer(monoStart),
			WallTime:  wallPointer(wallStart),
			Type:      network.ResourceTypeFetch,
		}, false)
	}
	archive, err := recorder.archive("same", har.Creator{Name: "test", Version: "1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(archive.Log.Entries) != 1 || archive.Log.Entries[0].Request.URL != "https://example.test/first" {
		t.Fatalf("entries = %+v", archive.Log.Entries)
	}
}

func TestEmptyExtraInfoHeadersRemainAuthoritative(t *testing.T) {
	t.Parallel()

	entry := &capturedEntry{
		request:      &network.Request{Headers: network.Headers{"X-Fallback": "request"}},
		requestExtra: &network.EventRequestWillBeSentExtraInfo{Headers: network.Headers{}},
		response: &network.Response{
			Headers:        network.Headers{"X-Fallback": "response"},
			RequestHeaders: network.Headers{"X-Fallback": "transmitted"},
		},
		responseExtra: &network.EventResponseReceivedExtraInfo{Headers: network.Headers{}, StatusCode: 304},
	}
	if got := requestHeaders(entry); len(got) != 0 {
		t.Fatalf("request headers = %+v, want empty", got)
	}
	if got := responseHeaders(entry); len(got) != 0 {
		t.Fatalf("response headers = %+v, want empty", got)
	}
}

func TestNameValuesSplitsDuplicateCDPHeaders(t *testing.T) {
	t.Parallel()

	got := nameValues(network.Headers{
		"Set-Cookie": "a=1\nb=2",
		"X-Number":   float64(3),
	})
	joined := make([]string, 0, len(got))
	for _, value := range got {
		joined = append(joined, value.Name+":"+value.Value)
	}
	if strings.Join(joined, ",") != "Set-Cookie:a=1,Set-Cookie:b=2,X-Number:3" {
		t.Fatalf("headers = %#v", got)
	}
	if got := jsonStringRetainedBytes("\x00"); got <= 2 {
		t.Fatalf("jsonStringRetainedBytes(NUL) = %d", got)
	}
	cookieHeaders := network.Headers{"Cookie": "a=1; b=2", "Set-Cookie": "c=3\nd=4"}
	if got, raw := headerRetainedBytes(cookieHeaders), int64(headerBytes(cookieHeaders)); got < raw+4*derivedCookieOverhead {
		t.Fatalf("cookie header retained bytes = %d, raw = %d", got, raw)
	}
	wantQuery := []har.NameValue{{Name: "q", Value: "one"}, {Name: "q", Value: "two"}}
	if gotQuery := queryString("https://example.test/?q=one&q=two"); !reflect.DeepEqual(gotQuery, wantQuery) {
		t.Fatalf("queryString() = %#v, want %#v", gotQuery, wantQuery)
	}
	if text, incomplete := postDataFromEntries([]*network.PostDataEntry{{Bytes: base64.StdEncoding.EncodeToString([]byte{0xff, 0x00})}}); text != "" || !incomplete {
		t.Fatalf("binary post data = %q, incomplete = %t", text, incomplete)
	}
}

func testMonotonicStart() time.Time {
	return cdp.MonotonicTimeEpoch.Add(time.Second)
}

func monotonicPointer(value time.Time) *cdp.MonotonicTime {
	converted := cdp.MonotonicTime(value)
	return &converted
}

func wallPointer(value time.Time) *cdp.TimeSinceEpoch {
	converted := cdp.TimeSinceEpoch(value)
	return &converted
}
