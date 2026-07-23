// Package cdphar captures HTTP Archive (HAR) 1.2 data from a Chromium browser
// exposed through the Chrome DevTools Protocol.
package cdphar

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto"
	"github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/cdproto/performance"
	"github.com/chromedp/chromedp"

	"gozillo/internal/har"
)

const (
	DefaultEndpoint = "http://127.0.0.1:9222"
	DefaultWait     = 5 * time.Second
	DefaultTimeout  = 45 * time.Second

	maxPostDataSize        = 10 << 20
	maxResourceBufferSize  = 10 << 20
	maxTotalBufferSize     = 100 << 20
	maxConcurrentFetches   = 8
	maxPendingFetches      = 64
	maxDiscoveryBytes      = 1 << 20
	extraInfoDrainTimeout  = 250 * time.Millisecond
	maxFreezeBufferBytes   = 16 << 20
	maxBrowserProductBytes = 256
)

// Options controls a CDP-backed HAR capture. Capture opens a new tab in the
// connected browser, navigates it to URL, waits for the page load plus Wait,
// and then returns the requests observed during that interval.
type Options struct {
	Endpoint              string
	URL                   string
	Wait                  time.Duration
	Timeout               time.Duration
	IncludeResponseBodies bool
	AllowRemoteEndpoint   bool
	CreatorName           string
	CreatorVersion        string
}

// Capture connects to an already-running Chromium browser and records one page
// navigation as a HAR 1.2 archive. The connected browser profile supplies its
// normal cookies and browser state to the new tab.
func Capture(parent context.Context, options Options) (*har.Archive, error) {
	options = options.withDefaults()
	if err := options.validate(); err != nil {
		return nil, err
	}
	if parent == nil {
		parent = context.Background()
	}

	captureCtx, cancelCapture := context.WithTimeout(parent, options.Timeout)
	defer cancelCapture()

	browserWebSocketURL, err := resolveBrowserWebSocketURL(captureCtx, options)
	if err != nil {
		return nil, err
	}

	allocatorCtx, cancelAllocator := chromedp.NewRemoteAllocator(captureCtx, browserWebSocketURL, chromedp.NoModifyURL)
	defer cancelAllocator()

	tabCtx, cancelTab := chromedp.NewContext(allocatorCtx)
	defer cancelTab()

	// Allocate and attach the target before installing the network listener.
	// The initial about:blank target does not create HTTP(S) HAR entries.
	if err := chromedp.Run(tabCtx); err != nil {
		return nil, fmt.Errorf("connect to CDP endpoint %s: %w", endpointLabel(options.Endpoint), redactError(err, options.Endpoint, browserWebSocketURL))
	}
	chromeContext := chromedp.FromContext(tabCtx)
	if chromeContext == nil || chromeContext.Target == nil || chromeContext.Browser == nil {
		return nil, errors.New("connect to CDP endpoint: browser target is unavailable")
	}
	targetCtx := cdp.WithExecutor(tabCtx, chromeContext.Target)
	browserCtx := cdp.WithExecutor(tabCtx, chromeContext.Browser)

	_, product, _, _, _, err := browser.GetVersion().Do(browserCtx)
	if err != nil {
		return nil, fmt.Errorf("read browser version: %w", err)
	}

	recorder := newRecorder(options.URL, time.Now().UTC())
	listenerCtx, cancelListener := context.WithCancel(tabCtx)
	defer cancelListener()

	var workerWG sync.WaitGroup
	var taskMu sync.Mutex
	taskQueue := make(chan fetchTask, maxPendingFetches)
	acceptTasks := true
	if options.IncludeResponseBodies {
		workerWG.Add(maxConcurrentFetches)
		for range maxConcurrentFetches {
			go func() {
				defer workerWG.Done()
				for task := range taskQueue {
					runFetchTask(targetCtx, recorder, task)
				}
			}()
		}
	}
	scheduleLocked := func(task fetchTask) {
		select {
		case taskQueue <- task:
		default:
			recorder.setResponseBody(task.entry, nil, errors.New("CDP response-body retrieval queue is full"))
		}
	}
	processEventLocked := func(event any, buffered bool) {
		if !acceptTasks {
			return
		}
		var tasks []fetchTask
		if buffered {
			tasks = recorder.handleBufferedEvent(event, options.IncludeResponseBodies)
		} else {
			tasks = recorder.handleEvent(event, options.IncludeResponseBodies)
		}
		for _, task := range tasks {
			scheduleLocked(task)
		}
	}
	freezing := false
	bufferedEvents := make([]any, 0, 16)
	var bufferedEventBytes int64

	chromedp.ListenTarget(listenerCtx, func(event any) {
		if _, isCommandResponse := event.(*cdproto.Message); isCommandResponse || !isRecorderEvent(event) {
			return
		}
		taskMu.Lock()
		defer taskMu.Unlock()
		if !acceptTasks {
			return
		}
		if freezing {
			eventBytes := freezeEventBytes(event)
			if eventBytes > maxFreezeBufferBytes-bufferedEventBytes {
				recorder.setCaptureError(errors.New("CDP event buffer exceeded its byte limit while establishing the capture cutoff"))
				return
			}
			bufferedEvents = append(bufferedEvents, event)
			bufferedEventBytes += eventBytes
			return
		}
		processEventLocked(event, false)
	})

	enableNetwork := networkEnableAction(options.IncludeResponseBodies)

	freezeAtCutoff := chromedp.ActionFunc(func(ctx context.Context) error {
		taskMu.Lock()
		freezing = true
		taskMu.Unlock()

		metrics, err := performance.GetMetrics().Do(ctx)
		var cutoff time.Time
		if err == nil {
			for _, metric := range metrics {
				if metric != nil && metric.Name == "Timestamp" {
					cutoff = cdp.MonotonicTimeEpoch.Add(time.Duration(metric.Value * float64(time.Second)))
					break
				}
			}
			if cutoff.IsZero() {
				err = errors.New("Performance.getMetrics did not return Timestamp")
			}
		}

		taskMu.Lock()
		defer taskMu.Unlock()
		freezing = false
		if err != nil {
			clearEventBuffer(bufferedEvents)
			bufferedEvents = nil
			bufferedEventBytes = 0
			return err
		}
		recorder.freezeAtMonotonic(cutoff)
		for _, event := range bufferedEvents {
			processEventLocked(event, true)
		}
		clearEventBuffer(bufferedEvents)
		bufferedEvents = nil
		bufferedEventBytes = 0
		return nil
	})
	// chromedp.Navigate is a NavigateAction: in v0.14.2 it waits for the
	// initiated main-frame navigation to finish loading before returning. The
	// configured wait then elapses, and the final action
	// freezes network intake at a browser-monotonic cutoff before extra-info drain.
	if err := chromedp.Run(tabCtx,
		enableNetwork,
		page.Enable(),
		performance.Enable().WithTimeDomain(performance.EnableTimeDomainTimeTicks),
		chromedp.Navigate(options.URL),
		chromedp.Sleep(options.Wait),
		freezeAtCutoff,
	); err != nil {
		cancelListener()
		stopAndWaitForTasks(&taskMu, &acceptTasks, taskQueue, &workerWG)
		return nil, fmt.Errorf("capture navigation through CDP: %w", redactError(err, options.URL, options.Endpoint, browserWebSocketURL))
	}

	waitForExtraInfo(captureCtx, recorder, extraInfoDrainTimeout)
	cancelListener()
	stopAndWaitForTasks(&taskMu, &acceptTasks, taskQueue, &workerWG)
	recorder.markMissingExtraInfo()
	if err := captureCtx.Err(); err != nil {
		return nil, fmt.Errorf("capture navigation through CDP: %w", err)
	}
	if err := recorder.captureError(); err != nil {
		return nil, err
	}

	archive, err := recorder.archive(
		"",
		har.Creator{Name: options.CreatorName, Version: options.CreatorVersion},
		browserCreator(product),
	)
	if err != nil {
		return nil, err
	}
	return archive, nil
}

func isRecorderEvent(event any) bool {
	switch event.(type) {
	case *network.EventDataReceived,
		*network.EventRequestWillBeSent,
		*network.EventRequestWillBeSentExtraInfo,
		*network.EventResponseReceived,
		*network.EventResponseReceivedExtraInfo,
		*network.EventRequestServedFromCache,
		*network.EventLoadingFinished,
		*network.EventLoadingFailed,
		*page.EventDomContentEventFired,
		*page.EventLoadEventFired:
		return true
	default:
		return false
	}
}

func freezeEventBytes(event any) int64 {
	encoded, err := json.Marshal(event)
	if err != nil {
		return maxFreezeBufferBytes + 1
	}
	return int64(len(encoded) + 1024)
}

func clearEventBuffer(events []any) {
	for index := range events {
		events[index] = nil
	}
}

func waitForExtraInfo(ctx context.Context, recorder *recorder, timeout time.Duration) {
	if !recorder.extraInfoDrainNeeded() {
		return
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			return
		case <-ticker.C:
			if !recorder.extraInfoDrainNeeded() {
				return
			}
		}
	}
}

func stopAndWaitForTasks(mu *sync.Mutex, accept *bool, queue chan fetchTask, wg *sync.WaitGroup) {
	mu.Lock()
	if *accept {
		*accept = false
		close(queue)
	}
	mu.Unlock()
	wg.Wait()
}

func runFetchTask(ctx context.Context, recorder *recorder, task fetchTask) {
	body, err := network.GetResponseBody(task.requestID).Do(ctx)
	recorder.setResponseBody(task.entry, body, err)
}

func (options Options) withDefaults() Options {
	if strings.TrimSpace(options.Endpoint) == "" {
		options.Endpoint = DefaultEndpoint
	}
	if options.Timeout == 0 {
		options.Timeout = DefaultTimeout
	}
	if strings.TrimSpace(options.CreatorName) == "" {
		options.CreatorName = "gozillo"
	}
	if strings.TrimSpace(options.CreatorVersion) == "" {
		options.CreatorVersion = "unknown"
	}
	return options
}

func (options Options) validate() error {
	endpoint, err := url.Parse(options.Endpoint)
	if err != nil || endpoint.Host == "" || endpoint.User != nil {
		return errors.New("capture HAR: invalid CDP endpoint")
	}
	switch strings.ToLower(endpoint.Scheme) {
	case "http":
		if endpoint.Path != "" && endpoint.Path != "/" {
			return errors.New("capture HAR: HTTP CDP endpoint must not include a path")
		}
		if !isLoopbackHost(endpoint.Hostname()) {
			return errors.New("capture HAR: remote CDP requires an exact browser WebSocket URL")
		}
	case "ws", "wss":
		if !isBrowserWebSocketPath(endpoint.Path) {
			return errors.New("capture HAR: WebSocket endpoint must be an exact browser WebSocket URL")
		}
	default:
		return errors.New("capture HAR: CDP endpoint scheme must be http, ws, or wss")
	}
	if !options.AllowRemoteEndpoint && !isLoopbackHost(endpoint.Hostname()) {
		return errors.New("capture HAR: CDP endpoint must be loopback unless remote access is explicitly allowed")
	}

	target, err := url.Parse(options.URL)
	if err != nil || target.Host == "" || target.User != nil {
		return errors.New("capture HAR: invalid URL")
	}
	if target.Scheme != "http" && target.Scheme != "https" {
		return errors.New("capture HAR: URL scheme must be http or https")
	}
	if options.Wait < 0 {
		return errors.New("capture HAR: wait duration must be non-negative")
	}
	if options.Timeout <= 0 {
		return errors.New("capture HAR: timeout must be positive")
	}
	if options.Wait >= options.Timeout {
		return errors.New("capture HAR: wait duration must be shorter than timeout")
	}
	return nil
}

func resolveBrowserWebSocketURL(ctx context.Context, options Options) (string, error) {
	endpoint, _ := url.Parse(options.Endpoint)
	if endpoint.Scheme == "ws" || endpoint.Scheme == "wss" {
		return options.Endpoint, nil
	}

	approvedAuthority := normalizedAuthority(endpoint)
	discoveryURL := *endpoint
	discoveryURL.Path = "/json/version"
	discoveryURL.RawPath = ""
	discoveryURL.Fragment = ""

	client := &http.Client{
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("too many CDP discovery redirects")
			}
			return validateDiscoveryHTTPURL(request.URL, options.AllowRemoteEndpoint, approvedAuthority)
		},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, discoveryURL.String(), nil)
	if err != nil {
		return "", fmt.Errorf("create CDP discovery request: %w", err)
	}
	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("discover browser WebSocket at %s: %w", endpointLabel(options.Endpoint), redactError(err, options.Endpoint, discoveryURL.String()))
	}
	defer response.Body.Close()
	if err := validateDiscoveryHTTPURL(response.Request.URL, options.AllowRemoteEndpoint, approvedAuthority); err != nil {
		return "", err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("discover browser WebSocket at %s: HTTP %s", endpointLabel(options.Endpoint), response.Status)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, maxDiscoveryBytes+1))
	if err != nil {
		return "", fmt.Errorf("read CDP discovery response: %w", err)
	}
	if len(body) > maxDiscoveryBytes {
		return "", errors.New("CDP discovery response is too large")
	}
	var version struct {
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	if err := json.Unmarshal(body, &version); err != nil {
		return "", fmt.Errorf("decode CDP discovery response: %w", err)
	}
	webSocketURL, err := url.Parse(strings.TrimSpace(version.WebSocketDebuggerURL))
	if err != nil || webSocketURL.Host == "" || webSocketURL.User != nil {
		return "", errors.New("CDP discovery returned an invalid browser WebSocket URL")
	}
	if webSocketURL.Scheme != "ws" && webSocketURL.Scheme != "wss" {
		return "", errors.New("CDP discovery returned a non-WebSocket browser URL")
	}
	if !isBrowserWebSocketPath(webSocketURL.Path) {
		return "", errors.New("CDP discovery returned a non-browser WebSocket URL")
	}
	if normalizedAuthority(webSocketURL) != approvedAuthority {
		return "", errors.New("CDP discovery returned a browser WebSocket URL with a different authority")
	}
	if !options.AllowRemoteEndpoint && !isLoopbackHost(webSocketURL.Hostname()) {
		return "", errors.New("CDP discovery returned a non-loopback browser WebSocket URL")
	}
	return webSocketURL.String(), nil
}

func validateDiscoveryHTTPURL(candidate *url.URL, allowRemote bool, approvedAuthority string) error {
	if candidate == nil || candidate.Scheme != "http" || candidate.Host == "" || candidate.User != nil {
		return errors.New("CDP discovery redirected to an invalid URL")
	}
	if normalizedAuthority(candidate) != approvedAuthority {
		return errors.New("CDP discovery changed authority")
	}
	if !allowRemote && !isLoopbackHost(candidate.Hostname()) {
		return errors.New("CDP discovery redirected to a non-loopback URL")
	}
	return nil
}

func normalizedAuthority(value *url.URL) string {
	if value == nil {
		return ""
	}
	host := strings.ToLower(value.Hostname())
	port := value.Port()
	if port == "" {
		switch strings.ToLower(value.Scheme) {
		case "http", "ws":
			port = "80"
		case "https", "wss":
			port = "443"
		}
	}
	if host == "" || port == "" {
		return ""
	}
	return net.JoinHostPort(host, port)
}

func isBrowserWebSocketPath(path string) bool {
	const prefix = "/devtools/browser/"
	return strings.HasPrefix(path, prefix) && strings.TrimPrefix(path, prefix) != ""
}

type durableNetworkEnableParams struct {
	MaxTotalBufferSize    int64 `json:"maxTotalBufferSize"`
	MaxResourceBufferSize int64 `json:"maxResourceBufferSize"`
	MaxPostDataSize       int64 `json:"maxPostDataSize"`
	EnableDurableMessages bool  `json:"enableDurableMessages"`
}

func networkEnableAction(includeResponseBodies bool) chromedp.Action {
	if !includeResponseBodies {
		return network.Enable().WithMaxPostDataSize(maxPostDataSize)
	}
	return chromedp.ActionFunc(func(ctx context.Context) error {
		return cdp.Execute(ctx, network.CommandEnable, &durableNetworkEnableParams{
			MaxTotalBufferSize:    maxTotalBufferSize,
			MaxResourceBufferSize: maxResourceBufferSize,
			MaxPostDataSize:       maxPostDataSize,
			EnableDurableMessages: true,
		}, nil)
	})
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(strings.TrimSpace(host), "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func redactError(err error, sensitiveValues ...string) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	var urlError *url.Error
	if errors.As(err, &urlError) {
		message = urlError.Op + ": " + urlError.Err.Error()
	}
	for _, value := range sensitiveValues {
		if value != "" {
			message = strings.ReplaceAll(message, value, "<redacted>")
		}
	}
	return errors.New(message)
}

func endpointLabel(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return "<invalid>"
	}
	return parsed.Scheme + "://" + parsed.Host
}

func browserCreator(product string) *har.Creator {
	product = strings.TrimSpace(product)
	if len(product) > maxBrowserProductBytes {
		product = product[:maxBrowserProductBytes]
	}
	if product == "" {
		return nil
	}
	name, version, found := strings.Cut(product, "/")
	if !found {
		return &har.Creator{Name: product, Version: "unknown"}
	}
	name = strings.TrimSpace(name)
	version = strings.TrimSpace(version)
	if name == "" {
		name = "Chromium"
	}
	if version == "" {
		version = "unknown"
	}
	return &har.Creator{Name: name, Version: version}
}
