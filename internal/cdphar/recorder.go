package cdphar

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"

	"gozillo/internal/har"
)

const (
	pageID                  = "page_1"
	defaultMaxEntries       = 10_000
	defaultMaxRetainedBytes = 128 << 20
	maxFrozenOrphanExtras   = 32
	maxHeaderItems          = 4096
	maxQueryItems           = 4096
	derivedItemOverhead     = 64
	derivedCookieOverhead   = 128
)

type fetchTask struct {
	requestID network.RequestID
	entry     *capturedEntry
}

type recorder struct {
	mu sync.Mutex

	targetURL     string
	captureStart  time.Time
	pageStart     time.Time
	pageStartMono time.Time
	cutoffMono    time.Time
	frozen        bool
	domContent    *time.Time
	load          *time.Time

	sequence             int
	chains               map[network.RequestID]*requestChain
	entries              []*capturedEntry
	maxEntries           int
	maxBytes             int64
	retainedBytes        int64
	limitErr             error
	frozenRequestExtras  map[network.RequestID][]*network.EventRequestWillBeSentExtraInfo
	frozenResponseExtras map[network.RequestID][]*network.EventResponseReceivedExtraInfo
	frozenOrphanCount    int
	postCutoffRequestIDs map[network.RequestID]bool
}

type requestChain struct {
	entries              []*capturedEntry
	pendingRequestExtra  []*network.EventRequestWillBeSentExtraInfo
	pendingResponseExtra []*network.EventResponseReceivedExtraInfo
}

type capturedEntry struct {
	sequence  int
	requestID network.RequestID

	request           *network.Request
	requestExtra      *network.EventRequestWillBeSentExtraInfo
	response          *network.Response
	responseExtra     *network.EventResponseReceivedExtraInfo
	extraInfoKnown    bool
	extraInfoExpected bool
	resourceType      network.ResourceType
	initiator         json.RawMessage
	queryString       []har.NameValue

	startedWall   time.Time
	startedMono   time.Time
	lastEventMono time.Time
	responseMono  time.Time
	finishedMono  time.Time

	encodedDataLength    float64
	loadingFinished      bool
	decodedBodyLength    int64
	encodedBodyLength    int64
	postData             string
	postDataIncomplete   bool
	responseBodyText     string
	responseBodyEncoding string
	responseBodySize     int64
	responseBodyPresent  bool
	bodyFetchScheduled   bool
	servedFromCache      bool
	failure              string
	terminalFailure      bool
	extraInfoIncomplete  bool
	responseBodyError    string
}

func newRecorder(targetURL string, captureStart time.Time) *recorder {
	recorder := &recorder{
		targetURL:            targetURL,
		captureStart:         captureStart,
		pageStart:            captureStart,
		chains:               make(map[network.RequestID]*requestChain),
		maxEntries:           defaultMaxEntries,
		maxBytes:             defaultMaxRetainedBytes,
		frozenRequestExtras:  make(map[network.RequestID][]*network.EventRequestWillBeSentExtraInfo),
		frozenResponseExtras: make(map[network.RequestID][]*network.EventResponseReceivedExtraInfo),
		postCutoffRequestIDs: make(map[network.RequestID]bool),
	}
	recorder.reserve(1024 + jsonStringRetainedBytes(targetURL))
	return recorder
}

func (recorder *recorder) handleBufferedEvent(event any, includeResponseBodies bool) []fetchTask {
	if cacheEvent, ok := event.(*network.EventRequestServedFromCache); ok {
		recorder.mu.Lock()
		defer recorder.mu.Unlock()
		if recorder.limitErr != nil || recorder.postCutoffRequestIDs[cacheEvent.RequestID] {
			return nil
		}
		if entry := recorder.currentEntry(cacheEvent.RequestID); entry != nil {
			entry.servedFromCache = true
		}
		return nil
	}
	return recorder.handleEvent(event, includeResponseBodies)
}

func (recorder *recorder) handleEvent(event any, includeResponseBodies bool) []fetchTask {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if recorder.limitErr != nil {
		return nil
	}
	if recorder.frozen {
		switch event := event.(type) {
		case *network.EventRequestWillBeSentExtraInfo:
			if recorder.hasCapturedRequest(event.RequestID) {
				recorder.requestExtraInfo(event)
			} else {
				recorder.queueFrozenRequestExtra(event)
			}
			return nil
		case *network.EventResponseReceivedExtraInfo:
			if recorder.hasCapturedRequest(event.RequestID) {
				recorder.responseExtraInfo(event)
				return recorder.readyBodyFetchTasks(event.RequestID, includeResponseBodies)
			}
			recorder.queueFrozenResponseExtra(event)
			return nil
		case *network.EventRequestServedFromCache:
			// This event has no timestamp or hop ordinal. After cutoff, ignoring it
			// is safer than applying a next-hop cache event to a captured redirect.
			return nil
		}
		timestamp, ok := eventTimestamp(event)
		if !ok || recorder.cutoffMono.IsZero() || timestamp.After(recorder.cutoffMono) {
			if request, isRequest := event.(*network.EventRequestWillBeSent); isRequest {
				recorder.postCutoffRequestIDs[request.RequestID] = true
			}
			return nil
		}
	}

	switch event := event.(type) {
	case *network.EventDataReceived:
		if entry := recorder.currentEntry(event.RequestID); entry != nil {
			entry.observe(monotonicTime(event.Timestamp))
			if event.DataLength > 0 {
				entry.decodedBodyLength += event.DataLength
			}
			if event.EncodedDataLength > 0 {
				entry.encodedBodyLength += event.EncodedDataLength
			}
		}
	case *network.EventRequestWillBeSent:
		return recorder.requestWillBeSent(event, includeResponseBodies)
	case *network.EventRequestWillBeSentExtraInfo:
		recorder.requestExtraInfo(event)
	case *network.EventResponseReceived:
		recorder.responseReceived(event)
		return recorder.readyBodyFetchTasks(event.RequestID, includeResponseBodies)
	case *network.EventResponseReceivedExtraInfo:
		recorder.responseExtraInfo(event)
		return recorder.readyBodyFetchTasks(event.RequestID, includeResponseBodies)
	case *network.EventRequestServedFromCache:
		if entry := recorder.currentEntry(event.RequestID); entry != nil {
			entry.servedFromCache = true
		}
	case *network.EventLoadingFinished:
		if entry := recorder.currentEntry(event.RequestID); entry != nil {
			entry.finishedMono = monotonicTime(event.Timestamp)
			entry.observe(entry.finishedMono)
			entry.encodedDataLength = event.EncodedDataLength
			entry.loadingFinished = true
		}
		return recorder.readyBodyFetchTasks(event.RequestID, includeResponseBodies)
	case *network.EventLoadingFailed:
		if entry := recorder.currentEntry(event.RequestID); entry != nil {
			entry.finishedMono = monotonicTime(event.Timestamp)
			entry.observe(entry.finishedMono)
			entry.terminalFailure = true
			failure := strings.TrimSpace(event.ErrorText)
			if failure == "" {
				failure = event.BlockedReason.String()
			}
			if !recorder.reserve(jsonStringRetainedBytes(failure) + derivedItemOverhead) {
				return nil
			}
			entry.failure = failure
			if chain := recorder.chains[event.RequestID]; chain != nil && !entry.extraInfoKnown && (len(chain.pendingRequestExtra) > 0 || len(chain.pendingResponseExtra) > 0) {
				recorder.setExtraInfoExpectation(chain, entry, true)
			}
		}
	case *page.EventDomContentEventFired:
		value := monotonicTime(event.Timestamp)
		recorder.domContent = &value
	case *page.EventLoadEventFired:
		value := monotonicTime(event.Timestamp)
		recorder.load = &value
	}
	return nil
}

func (recorder *recorder) requestWillBeSent(event *network.EventRequestWillBeSent, includeResponseBodies bool) []fetchTask {
	if event == nil || event.Request == nil || !isHTTPURL(event.Request.URL) {
		return nil
	}
	delete(recorder.postCutoffRequestIDs, event.RequestID)
	chain := recorder.chains[event.RequestID]
	if chain == nil {
		chain = &requestChain{}
		recorder.chains[event.RequestID] = chain
	}
	if len(chain.entries) == 0 {
		frozenRequests := recorder.frozenRequestExtras[event.RequestID]
		frozenResponses := recorder.frozenResponseExtras[event.RequestID]
		chain.pendingRequestExtra = append(chain.pendingRequestExtra, frozenRequests...)
		chain.pendingResponseExtra = append(chain.pendingResponseExtra, frozenResponses...)
		recorder.frozenOrphanCount -= len(frozenRequests) + len(frozenResponses)
		if recorder.frozenOrphanCount < 0 {
			recorder.frozenOrphanCount = 0
		}
		delete(recorder.frozenRequestExtras, event.RequestID)
		delete(recorder.frozenResponseExtras, event.RequestID)
	} else if event.RedirectResponse == nil {
		// Request IDs are unique except across redirect hops. Ignore duplicate
		// base events rather than creating a phantom hop that steals the response.
		return nil
	}

	startedMono := monotonicTime(event.Timestamp)
	startedWall := wallTime(event.WallTime, recorder.captureStart)
	if event.RedirectResponse != nil && len(chain.entries) > 0 {
		redirected := chain.entries[len(chain.entries)-1]
		if redirected.response == nil {
			if !recorder.validateHeaders(event.RedirectResponse.Headers, event.RedirectResponse.RequestHeaders) || !recorder.validateRequestCookieHeaders(event.RedirectResponse.RequestHeaders) {
				return nil
			}
			if !recorder.reserve(responseRetainedBytes(event.RedirectResponse)) {
				return nil
			}
			redirected.response = responseForHAR(event.RedirectResponse)
		}
		if redirected.finishedMono.IsZero() {
			redirected.finishedMono = startedMono
		}
		redirected.encodedDataLength = event.RedirectResponse.EncodedDataLength
		redirected.loadingFinished = true
		if includeResponseBodies {
			redirected.bodyFetchScheduled = true
			if !redirected.hasNoResponseBody() {
				redirected.responseBodyError = "CDP cannot reliably retrieve redirect response bodies after request ID reuse"
			}
		}
		recorder.setExtraInfoExpectation(chain, redirected, event.RedirectHasExtraInfo)
	}

	if !recorder.validateHeaders(event.Request.Headers) || !recorder.validateRequestCookieHeaders(event.Request.Headers) {
		return nil
	}
	queryValues, err := parseQueryString(event.Request.URL)
	if err != nil {
		recorder.setLimitError(err)
		return nil
	}

	postData := ""
	postDataIncomplete := false
	if event.Request.HasPostData {
		// The pinned cdproto Request type exposes PostDataEntries but not the
		// legacy postData string. Never perform a deferred RequestID lookup here:
		// redirects reuse IDs and could attach a later hop's body to this entry.
		postData, postDataIncomplete = postDataFromEntries(event.Request.PostDataEntries)
	}
	var initiator json.RawMessage
	if event.Initiator != nil {
		if encoded, err := json.Marshal(event.Initiator); err == nil {
			initiator = encoded
		}
	}
	estimatedBytes := int64(1024+2*len(initiator)) + jsonStringRetainedBytes(event.Request.URL) + jsonStringRetainedBytes(event.Request.Method) + jsonStringRetainedBytes(postData) + headerRetainedBytes(event.Request.Headers) + nameValueRetainedBytes(queryValues)
	if len(recorder.entries) >= recorder.maxEntries || !recorder.reserve(estimatedBytes) {
		if recorder.limitErr == nil {
			recorder.limitErr = fmt.Errorf("CDP HAR capture exceeded the retention limit of %d entries", recorder.maxEntries)
		}
		return nil
	}
	requestCopy := &network.Request{
		URL:         event.Request.URL,
		Method:      event.Request.Method,
		Headers:     event.Request.Headers,
		HasPostData: event.Request.HasPostData,
	}

	recorder.sequence++
	entry := &capturedEntry{
		sequence:           recorder.sequence,
		requestID:          event.RequestID,
		request:            requestCopy,
		resourceType:       event.Type,
		initiator:          initiator,
		queryString:        queryValues,
		startedWall:        startedWall,
		startedMono:        startedMono,
		lastEventMono:      startedMono,
		postData:           postData,
		postDataIncomplete: postDataIncomplete,
	}
	chain.entries = append(chain.entries, entry)
	recorder.entries = append(recorder.entries, entry)

	if recorder.pageStartMono.IsZero() && event.Type == network.ResourceTypeDocument {
		recorder.pageStartMono = startedMono
		recorder.pageStart = entry.startedWall
	}

	return nil
}

func (recorder *recorder) requestExtraInfo(event *network.EventRequestWillBeSentExtraInfo) {
	if event == nil {
		return
	}
	chain := recorder.chains[event.RequestID]
	if chain == nil {
		chain = &requestChain{}
		recorder.chains[event.RequestID] = chain
	}
	if !recorder.validateHeaders(event.Headers) || !recorder.validateRequestCookieHeaders(event.Headers) || !recorder.validateCookieCount(len(event.AssociatedCookies)) {
		return
	}
	if !recorder.reserve(requestExtraRetainedBytes(event)) {
		return
	}
	chain.pendingRequestExtra = append(chain.pendingRequestExtra, &network.EventRequestWillBeSentExtraInfo{
		RequestID:         event.RequestID,
		AssociatedCookies: event.AssociatedCookies,
		Headers:           event.Headers,
	})
	if entry := recorder.currentEntry(event.RequestID); entry != nil && entry.terminalFailure && !entry.extraInfoKnown {
		recorder.setExtraInfoExpectation(chain, entry, true)
		return
	}
	recorder.reconcileExtraInfo(chain)
}

func (recorder *recorder) responseReceived(event *network.EventResponseReceived) {
	if event == nil || event.Response == nil {
		return
	}
	entry := recorder.currentEntry(event.RequestID)
	if entry == nil {
		return
	}
	if !recorder.validateHeaders(event.Response.Headers, event.Response.RequestHeaders) || !recorder.validateRequestCookieHeaders(event.Response.RequestHeaders) {
		return
	}
	if !recorder.reserve(responseRetainedBytes(event.Response)) {
		return
	}
	entry.response = responseForHAR(event.Response)
	entry.responseMono = monotonicTime(event.Timestamp)
	entry.observe(entry.responseMono)
	if entry.resourceType == "" {
		entry.resourceType = event.Type
	}
	if chain := recorder.chains[event.RequestID]; chain != nil {
		recorder.setExtraInfoExpectation(chain, entry, event.HasExtraInfo)
	}
}

func (recorder *recorder) responseExtraInfo(event *network.EventResponseReceivedExtraInfo) {
	if event == nil {
		return
	}
	chain := recorder.chains[event.RequestID]
	if chain == nil {
		chain = &requestChain{}
		recorder.chains[event.RequestID] = chain
	}
	if !recorder.validateHeaders(event.Headers) {
		return
	}
	if !recorder.reserve(responseExtraRetainedBytes(event)) {
		return
	}
	chain.pendingResponseExtra = append(chain.pendingResponseExtra, &network.EventResponseReceivedExtraInfo{
		RequestID:   event.RequestID,
		Headers:     event.Headers,
		StatusCode:  event.StatusCode,
		HeadersText: event.HeadersText,
	})
	if entry := recorder.currentEntry(event.RequestID); entry != nil && entry.terminalFailure && !entry.extraInfoKnown {
		recorder.setExtraInfoExpectation(chain, entry, true)
		return
	}
	recorder.reconcileExtraInfo(chain)
}

func (recorder *recorder) readyBodyFetchTasks(requestID network.RequestID, includeResponseBodies bool) []fetchTask {
	if !includeResponseBodies {
		return nil
	}
	chain := recorder.chains[requestID]
	if chain == nil {
		return nil
	}
	tasks := make([]fetchTask, 0, 1)
	for _, entry := range chain.entries {
		if entry.bodyFetchScheduled || !entry.loadingFinished || entry.response == nil {
			continue
		}
		if entry.extraInfoExpected && entry.responseExtra == nil {
			continue
		}
		entry.bodyFetchScheduled = true
		if !entry.hasNoResponseBody() {
			tasks = append(tasks, fetchTask{requestID: requestID, entry: entry})
		}
	}
	return tasks
}

func (recorder *recorder) setExtraInfoExpectation(chain *requestChain, entry *capturedEntry, expected bool) {
	entry.extraInfoKnown = true
	entry.extraInfoExpected = expected
	recorder.reconcileExtraInfo(chain)
}

func (recorder *recorder) reconcileExtraInfo(chain *requestChain) {
	for _, entry := range chain.entries {
		if !entry.extraInfoKnown {
			break
		}
		if !entry.extraInfoExpected {
			continue
		}
		if entry.requestExtra == nil {
			if len(chain.pendingRequestExtra) == 0 {
				break
			}
			entry.requestExtra = chain.pendingRequestExtra[0]
			chain.pendingRequestExtra = chain.pendingRequestExtra[1:]
		}
	}
	for _, entry := range chain.entries {
		if !entry.extraInfoKnown {
			break
		}
		if !entry.extraInfoExpected {
			continue
		}
		if entry.responseExtra == nil {
			if len(chain.pendingResponseExtra) == 0 {
				break
			}
			entry.responseExtra = chain.pendingResponseExtra[0]
			chain.pendingResponseExtra = chain.pendingResponseExtra[1:]
		}
	}
}

func (recorder *recorder) queueFrozenRequestExtra(event *network.EventRequestWillBeSentExtraInfo) {
	if event == nil || recorder.frozenOrphanCount >= maxFrozenOrphanExtras {
		return
	}
	if !recorder.validateHeaders(event.Headers) || !recorder.validateRequestCookieHeaders(event.Headers) || !recorder.validateCookieCount(len(event.AssociatedCookies)) {
		return
	}
	if !recorder.reserve(requestExtraRetainedBytes(event)) {
		return
	}
	recorder.frozenOrphanCount++
	recorder.frozenRequestExtras[event.RequestID] = append(recorder.frozenRequestExtras[event.RequestID], &network.EventRequestWillBeSentExtraInfo{
		RequestID:         event.RequestID,
		AssociatedCookies: event.AssociatedCookies,
		Headers:           event.Headers,
	})
}

func (recorder *recorder) queueFrozenResponseExtra(event *network.EventResponseReceivedExtraInfo) {
	if event == nil || recorder.frozenOrphanCount >= maxFrozenOrphanExtras {
		return
	}
	if !recorder.validateHeaders(event.Headers) {
		return
	}
	if !recorder.reserve(responseExtraRetainedBytes(event)) {
		return
	}
	recorder.frozenOrphanCount++
	recorder.frozenResponseExtras[event.RequestID] = append(recorder.frozenResponseExtras[event.RequestID], &network.EventResponseReceivedExtraInfo{
		RequestID:   event.RequestID,
		Headers:     event.Headers,
		StatusCode:  event.StatusCode,
		HeadersText: event.HeadersText,
	})
}

func (recorder *recorder) hasCapturedRequest(requestID network.RequestID) bool {
	chain := recorder.chains[requestID]
	return chain != nil && len(chain.entries) > 0
}

func eventTimestamp(event any) (time.Time, bool) {
	switch event := event.(type) {
	case *network.EventDataReceived:
		return monotonicTime(event.Timestamp), event.Timestamp != nil
	case *network.EventRequestWillBeSent:
		return monotonicTime(event.Timestamp), event.Timestamp != nil
	case *network.EventResponseReceived:
		return monotonicTime(event.Timestamp), event.Timestamp != nil
	case *network.EventLoadingFinished:
		return monotonicTime(event.Timestamp), event.Timestamp != nil
	case *network.EventLoadingFailed:
		return monotonicTime(event.Timestamp), event.Timestamp != nil
	case *page.EventDomContentEventFired:
		return monotonicTime(event.Timestamp), event.Timestamp != nil
	case *page.EventLoadEventFired:
		return monotonicTime(event.Timestamp), event.Timestamp != nil
	default:
		return time.Time{}, false
	}
}

func (entry *capturedEntry) observe(value time.Time) {
	if !value.IsZero() && (entry.lastEventMono.IsZero() || value.After(entry.lastEventMono)) {
		entry.lastEventMono = value
	}
}

func (recorder *recorder) setLimitError(err error) {
	if err != nil && recorder.limitErr == nil {
		recorder.limitErr = err
	}
}

func (recorder *recorder) validateHeaders(collections ...network.Headers) bool {
	for _, headers := range collections {
		if headerItemCount(headers) > maxHeaderItems {
			recorder.setLimitError(fmt.Errorf("CDP HAR capture exceeded the per-message limit of %d header values", maxHeaderItems))
			return false
		}
	}
	return true
}

func (recorder *recorder) validateRequestCookieHeaders(headers network.Headers) bool {
	for name, value := range headers {
		if !strings.EqualFold(name, "Cookie") {
			continue
		}
		count := 0
		for _, text := range headerValueStrings(value) {
			if strings.TrimSpace(text) != "" {
				count += strings.Count(text, ";") + 1
			}
		}
		if count > maxHeaderItems {
			recorder.setLimitError(fmt.Errorf("CDP HAR capture exceeded the per-request limit of %d cookies", maxHeaderItems))
			return false
		}
	}
	return true
}

func (recorder *recorder) validateCookieCount(count int) bool {
	if count > maxHeaderItems {
		recorder.setLimitError(fmt.Errorf("CDP HAR capture exceeded the per-message limit of %d cookies", maxHeaderItems))
		return false
	}
	return true
}

func (recorder *recorder) reserve(size int64) bool {
	if size < 0 {
		size = 0
	}
	if recorder.limitErr != nil {
		return false
	}
	if size > recorder.maxBytes-recorder.retainedBytes {
		recorder.limitErr = fmt.Errorf("CDP HAR capture exceeded the retained-data limit of %d bytes", recorder.maxBytes)
		return false
	}
	recorder.retainedBytes += size
	return true
}

func (recorder *recorder) setCaptureError(err error) {
	if err == nil {
		return
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if recorder.limitErr == nil {
		recorder.limitErr = err
	}
}

func (recorder *recorder) captureError() error {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	return recorder.limitErr
}

func (recorder *recorder) freezeAtMonotonic(cutoff time.Time) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.frozen = true
	recorder.cutoffMono = cutoff
	recorder.finalizeQueuedExtraInfoLocked()
}

func (recorder *recorder) finalizeQueuedExtraInfoLocked() {
	for _, chain := range recorder.chains {
		recorder.reconcileExtraInfo(chain)
	}
}

func (recorder *recorder) extraInfoDrainNeeded() bool {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.finalizeQueuedExtraInfoLocked()
	if len(recorder.frozenRequestExtras) > 0 || len(recorder.frozenResponseExtras) > 0 {
		return true
	}
	for _, chain := range recorder.chains {
		if len(chain.entries) == 0 && (len(chain.pendingRequestExtra) > 0 || len(chain.pendingResponseExtra) > 0) {
			return true
		}
	}
	for _, entry := range recorder.entries {
		if !entry.extraInfoKnown || entry.extraInfoExpected && (entry.requestExtra == nil || entry.responseExtra == nil) {
			return true
		}
	}
	return false
}

func (recorder *recorder) pendingExtraInfoCount() int {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.finalizeQueuedExtraInfoLocked()
	count := 0
	for _, entry := range recorder.entries {
		if entry.extraInfoExpected {
			if entry.requestExtra == nil {
				count++
			}
			if entry.responseExtra == nil {
				count++
			}
		}
	}
	return count
}

func (recorder *recorder) markMissingExtraInfo() {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	recorder.finalizeQueuedExtraInfoLocked()
	for _, entry := range recorder.entries {
		entry.extraInfoIncomplete = !entry.extraInfoKnown || entry.extraInfoExpected && (entry.requestExtra == nil || entry.responseExtra == nil)
	}
}

func (recorder *recorder) orphanExtraCountLocked() int {
	count := 0
	for _, events := range recorder.frozenRequestExtras {
		count += len(events)
	}
	for _, events := range recorder.frozenResponseExtras {
		count += len(events)
	}
	for _, chain := range recorder.chains {
		if len(chain.entries) == 0 {
			count += len(chain.pendingRequestExtra) + len(chain.pendingResponseExtra)
		}
	}
	return count
}

func (recorder *recorder) currentEntry(requestID network.RequestID) *capturedEntry {
	chain := recorder.chains[requestID]
	if chain == nil || len(chain.entries) == 0 {
		return nil
	}
	return chain.entries[len(chain.entries)-1]
}

func (recorder *recorder) setResponseBody(entry *capturedEntry, body []byte, err error) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if entry == nil {
		return
	}
	if err != nil {
		entry.responseBodyError = err.Error()
		return
	}
	mimeType := entry.responseMimeType()
	text := ""
	encoding := ""
	if responseBodyNeedsBase64(mimeType, body) {
		text = base64.StdEncoding.EncodeToString(body)
		encoding = "base64"
	} else {
		text = string(body)
	}
	if !recorder.reserve(jsonStringRetainedBytes(text) + derivedItemOverhead) {
		entry.responseBodyError = "response body omitted because the capture retention limit was reached"
		return
	}
	entry.responseBodyText = text
	entry.responseBodyEncoding = encoding
	entry.responseBodySize = int64(len(body))
	entry.responseBodyPresent = true
}

func (recorder *recorder) archive(title string, creator har.Creator, browser *har.Creator) (*har.Archive, error) {
	recorder.mu.Lock()
	defer recorder.mu.Unlock()

	recorder.finalizeQueuedExtraInfoLocked()

	entries := append([]*capturedEntry(nil), recorder.entries...)
	sort.SliceStable(entries, func(left, right int) bool {
		return entries[left].sequence < entries[right].sequence
	})

	logComment := "CDP capture covers the primary page target; child-target worker and out-of-process iframe traffic may be omitted."
	orphanExtras := recorder.orphanExtraCountLocked()
	if orphanExtras > 0 {
		logComment += fmt.Sprintf(" %d supplemental CDP event(s) had no matching base request before the drain ended.", orphanExtras)
	}

	archive := &har.Archive{Log: har.Log{
		Version: har.Version12,
		Creator: creator,
		Browser: browser,
		Comment: logComment,
		Pages: []har.Page{{
			StartedDateTime: recorder.pageStart.UTC().Format(time.RFC3339Nano),
			ID:              pageID,
			Title:           strings.TrimSpace(title),
			PageTimings:     recorder.pageTimings(),
		}},
		Entries: make([]har.Entry, 0, len(entries)),
	}}
	if archive.Log.Pages[0].Title == "" {
		archive.Log.Pages[0].Title = recorder.targetURL
	}

	for _, captured := range entries {
		archive.Log.Entries = append(archive.Log.Entries, captured.toHAR(recorder.cutoffMono))
	}
	if err := archive.Validate(); err != nil {
		return nil, fmt.Errorf("capture HAR: %w", err)
	}
	return archive, nil
}

func (recorder *recorder) pageTimings() har.PageTimings {
	var result har.PageTimings
	if recorder.pageStartMono.IsZero() {
		return result
	}
	if recorder.domContent != nil {
		value := durationMilliseconds(recorder.pageStartMono, *recorder.domContent)
		result.OnContentLoad = &value
	}
	if recorder.load != nil {
		value := durationMilliseconds(recorder.pageStartMono, *recorder.load)
		result.OnLoad = &value
	}
	return result
}

func (entry *capturedEntry) toHAR(cutoffMono time.Time) har.Entry {
	finishMono := entry.finishedMono
	if finishMono.IsZero() && !cutoffMono.IsZero() && !cutoffMono.Before(entry.startedMono) {
		finishMono = cutoffMono
	}
	if finishMono.IsZero() {
		finishMono = entry.lastEventMono
	}
	if finishMono.IsZero() {
		finishMono = entry.responseMono
	}
	if finishMono.IsZero() {
		finishMono = entry.startedMono
	}
	if finishMono.Before(entry.startedMono) {
		finishMono = entry.startedMono
	}

	requestHeaders := requestHeaders(entry)
	responseHeaders := responseHeaders(entry)
	requestCookies := requestCookies(entry, requestHeaders)
	responseCookies := cookiesFromResponseHeaders(responseHeaders)
	postData := entry.harPostData(requestHeaders)
	requestBodySize := int64(0)
	if postData != nil {
		requestBodySize = int64(len([]byte(postData.Text)))
		if entry.postDataIncomplete {
			requestBodySize = -1
		}
	}

	response := entry.harResponse(responseHeaders, responseCookies)
	total := durationMilliseconds(entry.startedMono, finishMono)
	result := har.Entry{
		PageRef:         pageID,
		StartedDateTime: entry.startedWall.UTC().Format(time.RFC3339Nano),
		Time:            total,
		Request: har.Request{
			Method:      entry.request.Method,
			URL:         entry.request.URL,
			HTTPVersion: response.HTTPVersion,
			Headers:     requestHeaders,
			QueryString: entry.queryString,
			Cookies:     requestCookies,
			HeadersSize: -1,
			BodySize:    requestBodySize,
			PostData:    postData,
		},
		Response:     response,
		Cache:        json.RawMessage(`{}`),
		Timings:      entry.harTimings(finishMono),
		ResourceType: entry.resourceType.String(),
		Initiator:    entry.initiator,
	}
	if entry.response != nil {
		result.ServerIPAddress = entry.response.RemoteIPAddress
		if entry.response.ConnectionID != 0 {
			result.Connection = strconv.FormatFloat(entry.response.ConnectionID, 'f', -1, 64)
		}
	}
	comments := make([]string, 0, 4)
	if entry.isFromCache() {
		comments = append(comments, "Served from browser cache.")
	}
	if entry.failure != "" {
		comments = append(comments, "CDP loading failure: "+entry.failure)
	} else if !entry.loadingFinished {
		comments = append(comments, "Request was still in flight at the capture cutoff; timing and sizes are incomplete.")
	}
	if entry.extraInfoIncomplete {
		comments = append(comments, "CDP supplemental request/response metadata did not arrive before the capture cutoff.")
	}
	result.Comment = strings.Join(comments, " ")
	return result
}

func (entry *capturedEntry) responseMimeType() string {
	if entry.responseExtra != nil {
		if value := firstNetworkHeader(entry.responseExtra.Headers, "Content-Type"); value != "" {
			return value
		}
	}
	if entry.response != nil {
		if value := firstNetworkHeader(entry.response.Headers, "Content-Type"); value != "" {
			return value
		}
		return entry.response.MimeType
	}
	return ""
}

func firstNetworkHeader(headers network.Headers, name string) string {
	for headerName, value := range headers {
		if strings.EqualFold(headerName, name) {
			values := headerValueStrings(value)
			if len(values) > 0 {
				return values[0]
			}
		}
	}
	return ""
}

func (entry *capturedEntry) isFromCache() bool {
	return entry.servedFromCache || entry.response != nil && (entry.response.FromDiskCache || entry.response.FromPrefetchCache)
}

func (entry *capturedEntry) hasNoResponseBody() bool {
	status := 0
	if entry.response != nil {
		status = int(entry.response.Status)
	}
	if entry.responseExtra != nil && entry.responseExtra.StatusCode != 0 {
		status = int(entry.responseExtra.StatusCode)
	}
	return entry.request != nil && responseHasNoBody(entry.request.Method, status)
}

func (entry *capturedEntry) harPostData(headers []har.NameValue) *har.PostData {
	if entry.request == nil || (!entry.request.HasPostData && entry.postData == "") {
		return nil
	}
	postData := &har.PostData{
		MimeType: firstHeader(headers, "Content-Type"),
		Text:     entry.postData,
	}
	if entry.postDataIncomplete {
		postData.Comment = "CDP did not provide a complete UTF-8 request body; multipart file bytes and binary payloads are not captured."
	}
	return postData
}

func (entry *capturedEntry) harResponse(headers []har.NameValue, cookies []har.Cookie) har.Response {
	status := 0
	statusText := ""
	protocol := ""
	mimeType := firstHeader(headers, "Content-Type")
	redirectURL := firstHeader(headers, "Location")
	bodySize := int64(-1)
	contentSize := int64(0)
	headersSize := int64(-1)
	if entry.response != nil {
		status = int(entry.response.Status)
		statusText = entry.response.StatusText
		protocol = entry.response.Protocol
		if mimeType == "" && entry.response.MimeType != "" {
			mimeType = entry.response.MimeType
		}
	}
	if entry.responseExtra != nil {
		if entry.responseExtra.StatusCode != 0 {
			extraStatus := int(entry.responseExtra.StatusCode)
			if extraStatus != status {
				statusText = http.StatusText(extraStatus)
			}
			status = extraStatus
		}
		if entry.responseExtra.HeadersText != "" {
			headersSize = int64(len([]byte(entry.responseExtra.HeadersText)))
		}
	}
	if entry.encodedBodyLength > 0 {
		bodySize = entry.encodedBodyLength
	} else if entry.encodedDataLength > 0 && headersSize >= 0 && int64(entry.encodedDataLength) >= headersSize {
		bodySize = int64(entry.encodedDataLength) - headersSize
	}
	if bodySize < 0 && entry.loadingFinished {
		if contentLength, ok := responseContentLength(headers); ok {
			bodySize = contentLength
		}
	}
	if entry.decodedBodyLength > 0 {
		contentSize = entry.decodedBodyLength
	} else if contentLength, ok := responseContentLength(headers); ok && entry.loadingFinished {
		contentSize = contentLength
	} else if bodySize >= 0 {
		contentSize = bodySize
	}
	noBody := responseHasNoBody(entry.request.Method, status)
	if noBody {
		bodySize = 0
		contentSize = 0
	} else if entry.isFromCache() {
		bodySize = 0
	}
	content := har.Content{Size: contentSize, MimeType: mimeType}
	if !noBody && entry.responseBodyPresent {
		content.Size = entry.responseBodySize
		content.Text = entry.responseBodyText
		content.Encoding = entry.responseBodyEncoding
	}
	if !noBody && entry.responseBodyError != "" {
		content.Comment = "CDP could not retrieve response body: " + entry.responseBodyError
	}
	return har.Response{
		Status:      status,
		StatusText:  statusText,
		HTTPVersion: httpVersion(protocol),
		Headers:     headers,
		Cookies:     cookies,
		Content:     content,
		RedirectURL: redirectURL,
		HeadersSize: headersSize,
		BodySize:    bodySize,
	}
}

func (entry *capturedEntry) harTimings(finish time.Time) *har.Timings {
	result := &har.Timings{}
	if entry.response != nil && entry.response.Timing != nil {
		timing := entry.response.Timing
		result.DNS = durationPointer(timing.DNSStart, timing.DNSEnd)
		result.Connect = durationPointer(timing.ConnectStart, timing.ConnectEnd)
		result.SSL = durationPointer(timing.SslStart, timing.SslEnd)
		result.Send = nonNegative(timing.SendEnd - timing.SendStart)
		result.Wait = nonNegative(timing.ReceiveHeadersEnd - timing.SendEnd)
		finishSeconds := monotonicSeconds(finish)
		headersSeconds := timing.RequestTime + timing.ReceiveHeadersEnd/1000
		if timing.RequestTime > 0 && finishSeconds >= headersSeconds {
			result.Receive = (finishSeconds - headersSeconds) * 1000
		} else if !entry.responseMono.IsZero() {
			result.Receive = durationMilliseconds(entry.responseMono, finish)
		}

		// CDP phases can have gaps, while SSL is nested inside connect. HAR's
		// blocked phase carries all otherwise-unassigned elapsed time so the
		// non-SSL phase sum remains equal to Entry.Time without overlap.
		assigned := result.Send + result.Wait + result.Receive
		if result.DNS != nil {
			assigned += *result.DNS
		}
		if result.Connect != nil {
			assigned += *result.Connect
		}
		blocked := nonNegative(durationMilliseconds(entry.startedMono, finish) - assigned)
		result.Blocked = &blocked
		return result
	}
	if !entry.responseMono.IsZero() {
		result.Wait = durationMilliseconds(entry.startedMono, entry.responseMono)
		result.Receive = durationMilliseconds(entry.responseMono, finish)
	} else {
		result.Wait = durationMilliseconds(entry.startedMono, finish)
	}
	return result
}

func requestHeaders(entry *capturedEntry) []har.NameValue {
	if entry.requestExtra != nil {
		return nameValues(entry.requestExtra.Headers)
	}
	if entry.response != nil && len(entry.response.RequestHeaders) > 0 {
		return nameValues(entry.response.RequestHeaders)
	}
	if entry.request == nil {
		return []har.NameValue{}
	}
	return nameValues(entry.request.Headers)
}

func responseHeaders(entry *capturedEntry) []har.NameValue {
	if entry.responseExtra != nil {
		return nameValues(entry.responseExtra.Headers)
	}
	if entry.response == nil {
		return []har.NameValue{}
	}
	return nameValues(entry.response.Headers)
}

func requestCookies(entry *capturedEntry, headers []har.NameValue) []har.Cookie {
	cookies := make([]har.Cookie, 0)
	if entry.requestExtra != nil && len(entry.requestExtra.AssociatedCookies) > 0 {
		complete := true
		for _, associated := range entry.requestExtra.AssociatedCookies {
			if associated == nil || associated.Cookie == nil {
				complete = false
				break
			}
			if len(associated.BlockedReasons) == 0 {
				cookies = append(cookies, cookieFromCDP(associated.Cookie))
			}
		}
		if complete {
			return cookies
		}
		cookies = cookies[:0]
	}
	for _, value := range headerValues(headers, "Cookie") {
		request := &http.Request{Header: http.Header{"Cookie": []string{value}}}
		for _, cookie := range request.Cookies() {
			cookies = append(cookies, har.Cookie{Name: cookie.Name, Value: cookie.Value})
		}
	}
	return cookies
}

func cookieFromCDP(cookie *network.Cookie) har.Cookie {
	result := har.Cookie{
		Name:     cookie.Name,
		Value:    cookie.Value,
		Path:     cookie.Path,
		Domain:   cookie.Domain,
		HTTPOnly: cookie.HTTPOnly,
		Secure:   cookie.Secure,
		SameSite: cookie.SameSite.String(),
	}
	if !cookie.Session && cookie.Expires > 0 {
		result.Expires = time.Unix(0, int64(cookie.Expires*float64(time.Second))).UTC().Format(time.RFC3339Nano)
	}
	return result
}

func cookiesFromResponseHeaders(headers []har.NameValue) []har.Cookie {
	response := &http.Response{Header: make(http.Header)}
	for _, value := range headerValues(headers, "Set-Cookie") {
		response.Header.Add("Set-Cookie", value)
	}
	parsed := response.Cookies()
	cookies := make([]har.Cookie, 0, len(parsed))
	for _, cookie := range parsed {
		item := har.Cookie{
			Name:     cookie.Name,
			Value:    cookie.Value,
			Path:     cookie.Path,
			Domain:   cookie.Domain,
			HTTPOnly: cookie.HttpOnly,
			Secure:   cookie.Secure,
			SameSite: sameSiteString(cookie.SameSite),
		}
		if !cookie.Expires.IsZero() {
			item.Expires = cookie.Expires.UTC().Format(time.RFC3339Nano)
		}
		cookies = append(cookies, item)
	}
	return cookies
}

func nameValues(headers network.Headers) []har.NameValue {
	keys := make([]string, 0, len(headers))
	for name := range headers {
		keys = append(keys, name)
	}
	sort.SliceStable(keys, func(left, right int) bool {
		leftName := strings.ToLower(keys[left])
		rightName := strings.ToLower(keys[right])
		if leftName == rightName {
			return keys[left] < keys[right]
		}
		return leftName < rightName
	})
	result := make([]har.NameValue, 0, len(keys))
	for _, name := range keys {
		for _, value := range headerValueStrings(headers[name]) {
			result = append(result, har.NameValue{Name: name, Value: value})
		}
	}
	return result
}

func headerValueStrings(value any) []string {
	switch value := value.(type) {
	case string:
		return strings.Split(value, "\n")
	case []string:
		return append([]string(nil), value...)
	case []any:
		result := make([]string, 0, len(value))
		for _, item := range value {
			result = append(result, fmt.Sprint(item))
		}
		return result
	default:
		return []string{fmt.Sprint(value)}
	}
}

func responseForHAR(response *network.Response) *network.Response {
	if response == nil {
		return nil
	}
	return &network.Response{
		URL:               response.URL,
		Status:            response.Status,
		StatusText:        response.StatusText,
		Headers:           response.Headers,
		MimeType:          response.MimeType,
		RequestHeaders:    response.RequestHeaders,
		ConnectionID:      response.ConnectionID,
		RemoteIPAddress:   response.RemoteIPAddress,
		FromDiskCache:     response.FromDiskCache,
		FromPrefetchCache: response.FromPrefetchCache,
		EncodedDataLength: response.EncodedDataLength,
		Timing:            response.Timing,
		Protocol:          response.Protocol,
	}
}

func responseRetainedBytes(response *network.Response) int64 {
	if response == nil {
		return 0
	}
	return 1024 + jsonStringRetainedBytes(response.URL) + jsonStringRetainedBytes(response.StatusText) + jsonStringRetainedBytes(response.MimeType) + jsonStringRetainedBytes(response.Protocol) + jsonStringRetainedBytes(response.RemoteIPAddress) + headerRetainedBytes(response.Headers) + headerRetainedBytes(response.RequestHeaders)
}

func requestExtraRetainedBytes(event *network.EventRequestWillBeSentExtraInfo) int64 {
	if event == nil {
		return 0
	}
	return 512 + associatedCookieBytes(event.AssociatedCookies) + headerRetainedBytes(event.Headers)
}

func responseExtraRetainedBytes(event *network.EventResponseReceivedExtraInfo) int64 {
	if event == nil {
		return 0
	}
	return int64(512+len(event.HeadersText)) + headerRetainedBytes(event.Headers)
}

func headerBytes(headers network.Headers) int {
	total := 0
	for name, value := range headers {
		total += len(name)
		switch value := value.(type) {
		case string:
			total += len(value)
		case []string:
			for _, text := range value {
				total += len(text)
			}
		case []any:
			for _, item := range value {
				total += len(fmt.Sprint(item))
			}
		default:
			total += len(fmt.Sprint(value))
		}
	}
	return total
}

func headerItemCount(headers network.Headers) int {
	count := 0
	for _, value := range headers {
		switch value := value.(type) {
		case string:
			count += strings.Count(value, "\n") + 1
		case []string:
			count += len(value)
		case []any:
			count += len(value)
		default:
			count++
		}
		if count > maxHeaderItems {
			return count
		}
	}
	return count
}

func headerRetainedBytes(headers network.Headers) int64 {
	total := int64(headerItemCount(headers) * derivedItemOverhead)
	for name, rawValue := range headers {
		values := headerValueStrings(rawValue)
		for _, value := range values {
			total += jsonStringRetainedBytes(name) + jsonStringRetainedBytes(value)
			if strings.EqualFold(name, "Cookie") || strings.EqualFold(name, "Set-Cookie") {
				// Cookie objects repeat parsed portions of the raw header in HAR.
				total += jsonStringRetainedBytes(value)
			}
		}
	}
	total += int64(derivedCookieCount(headers) * derivedCookieOverhead)
	return total
}

func derivedCookieCount(headers network.Headers) int {
	count := 0
	for name, value := range headers {
		switch {
		case strings.EqualFold(name, "Cookie"):
			for _, text := range headerValueStrings(value) {
				if strings.TrimSpace(text) != "" {
					count += strings.Count(text, ";") + 1
				}
			}
		case strings.EqualFold(name, "Set-Cookie"):
			count += headerValueCount(value)
		}
	}
	return count
}

func headerValueCount(value any) int {
	switch value := value.(type) {
	case string:
		return strings.Count(value, "\n") + 1
	case []string:
		return len(value)
	case []any:
		return len(value)
	default:
		return 1
	}
}

func jsonStringRetainedBytes(value string) int64 {
	encoded, err := json.Marshal(value)
	if err != nil {
		return int64(len(value))
	}
	return int64(len(value) + len(encoded))
}

func nameValueRetainedBytes(values []har.NameValue) int64 {
	total := int64(len(values) * derivedItemOverhead)
	for _, value := range values {
		total += jsonStringRetainedBytes(value.Name) + jsonStringRetainedBytes(value.Value)
	}
	return total
}

func associatedCookieBytes(cookies []*network.AssociatedCookie) int64 {
	total := int64(0)
	for _, associated := range cookies {
		if associated == nil || associated.Cookie == nil {
			continue
		}
		cookie := associated.Cookie
		total += jsonStringRetainedBytes(cookie.Name) + jsonStringRetainedBytes(cookie.Value) + jsonStringRetainedBytes(cookie.Domain) + jsonStringRetainedBytes(cookie.Path) + derivedCookieOverhead
	}
	return total
}

func parseQueryString(rawURL string) ([]har.NameValue, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.RawQuery == "" {
		return []har.NameValue{}, err
	}
	itemCount := strings.Count(parsed.RawQuery, "&") + 1
	if itemCount > maxQueryItems {
		return nil, fmt.Errorf("CDP HAR capture exceeded the per-request limit of %d query items", maxQueryItems)
	}
	result := make([]har.NameValue, 0, itemCount)
	for _, pair := range strings.Split(parsed.RawQuery, "&") {
		name, value, found := strings.Cut(pair, "=")
		decodedName, nameErr := url.QueryUnescape(name)
		decodedValue, valueErr := url.QueryUnescape(value)
		if nameErr == nil {
			name = decodedName
		}
		if found && valueErr == nil {
			value = decodedValue
		}
		result = append(result, har.NameValue{Name: name, Value: value})
	}
	return result, nil
}

func queryString(rawURL string) []har.NameValue {
	values, _ := parseQueryString(rawURL)
	return values
}

func postDataFromEntries(entries []*network.PostDataEntry) (string, bool) {
	if len(entries) == 0 {
		return "", true
	}
	complete := true
	var data []byte
	for _, entry := range entries {
		if entry == nil || entry.Bytes == "" {
			complete = false
			continue
		}
		decoded, err := base64.StdEncoding.DecodeString(entry.Bytes)
		if err != nil {
			complete = false
			continue
		}
		data = append(data, decoded...)
	}
	if !utf8.Valid(data) {
		return "", true
	}
	return string(data), !complete
}

func responseHasNoBody(method string, status int) bool {
	if strings.EqualFold(method, http.MethodHead) {
		return true
	}
	return status >= 100 && status < 200 || status == http.StatusNoContent || status == http.StatusResetContent || status == http.StatusNotModified
}

func responseBodyNeedsBase64(mimeType string, body []byte) bool {
	if !utf8.Valid(body) {
		return true
	}
	mediaType := strings.ToLower(strings.TrimSpace(strings.SplitN(mimeType, ";", 2)[0]))
	if strings.HasPrefix(mediaType, "text/") {
		return false
	}
	for _, textual := range []string{"json", "xml", "javascript", "ecmascript", "x-www-form-urlencoded", "svg"} {
		if strings.Contains(mediaType, textual) {
			return false
		}
	}
	return mediaType != ""
}

func responseContentLength(headers []har.NameValue) (int64, bool) {
	value := strings.TrimSpace(firstHeader(headers, "Content-Length"))
	if value == "" {
		return 0, false
	}
	length, err := strconv.ParseInt(value, 10, 64)
	if err != nil || length < 0 {
		return 0, false
	}
	return length, true
}

func firstHeader(headers []har.NameValue, name string) string {
	for _, header := range headers {
		if strings.EqualFold(header.Name, name) {
			return header.Value
		}
	}
	return ""
}

func headerValues(headers []har.NameValue, name string) []string {
	values := make([]string, 0)
	for _, header := range headers {
		if strings.EqualFold(header.Name, name) {
			values = append(values, header.Value)
		}
	}
	return values
}

func httpVersion(protocol string) string {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case "h2", "http/2", "http/2.0":
		return "HTTP/2"
	case "h3", "http/3", "http/3.0", "quic":
		return "HTTP/3"
	case "http/1.0":
		return "HTTP/1.0"
	case "http/1.1":
		return "HTTP/1.1"
	case "":
		return ""
	default:
		return protocol
	}
}

func sameSiteString(value http.SameSite) string {
	switch value {
	case http.SameSiteLaxMode:
		return "Lax"
	case http.SameSiteStrictMode:
		return "Strict"
	case http.SameSiteNoneMode:
		return "None"
	default:
		return ""
	}
}

func durationPointer(start, end float64) *float64 {
	if start < 0 || end < start {
		return nil
	}
	value := end - start
	return &value
}

func durationMilliseconds(start, end time.Time) float64 {
	if start.IsZero() || end.IsZero() || end.Before(start) {
		return 0
	}
	return float64(end.Sub(start)) / float64(time.Millisecond)
}

func nonNegative(value float64) float64 {
	if value < 0 {
		return 0
	}
	return value
}

func monotonicSeconds(value time.Time) float64 {
	if value.IsZero() || cdp.MonotonicTimeEpoch == nil {
		return 0
	}
	return float64(value.Sub(*cdp.MonotonicTimeEpoch)) / float64(time.Second)
}

func monotonicTime(value *cdp.MonotonicTime) time.Time {
	if value == nil {
		return time.Time{}
	}
	return value.Time()
}

func wallTime(value *cdp.TimeSinceEpoch, fallback time.Time) time.Time {
	if value == nil {
		return fallback
	}
	return value.Time().UTC()
}

func isHTTPURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https")
}
