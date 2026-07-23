package cli

import (
	"fmt"
	"io"
	"sync"
	"time"

	"gozillo/internal/zillow"
)

type searchProgressLogger struct {
	enabled bool
	writer  io.Writer
	started time.Time
	mu      sync.Mutex
}

func newSearchProgressLogger(enabled bool, writer io.Writer) *searchProgressLogger {
	return &searchProgressLogger{enabled: enabled, writer: writer, started: time.Now()}
}

func (logger *searchProgressLogger) printf(format string, args ...any) {
	if logger == nil || !logger.enabled || logger.writer == nil {
		return
	}
	logger.mu.Lock()
	defer logger.mu.Unlock()
	elapsed := time.Since(logger.started).Round(time.Second)
	_, _ = fmt.Fprintf(logger.writer, "progress +%s: ", elapsed)
	_, _ = fmt.Fprintf(logger.writer, format, args...)
	_, _ = fmt.Fprintln(logger.writer)
}

func (logger *searchProgressLogger) discovery(locationLabel string, event zillow.DiscoveryProgress) {
	switch event.Stage {
	case zillow.DiscoveryProgressBootstrap:
		logger.printf("%s: bootstrap request", locationLabel)
	case zillow.DiscoveryProgressPage:
		if event.Delay > 0 {
			logger.printf("%s: waiting %s, then route %d/%d %q page %d/%d", locationLabel, event.Delay, event.RouteIndex, event.RouteCount, event.Route, event.Page, event.PageLimit)
			return
		}
		logger.printf("%s: route %d/%d %q page %d/%d", locationLabel, event.RouteIndex, event.RouteCount, event.Route, event.Page, event.PageLimit)
	case zillow.DiscoveryProgressRetry:
		scope := "bootstrap"
		if event.Route != "" {
			scope = fmt.Sprintf("route %d/%d %q page %d/%d", event.RouteIndex, event.RouteCount, event.Route, event.Page, event.PageLimit)
		}
		logger.printf("%s: %s retry %d/%d in %s after %v", locationLabel, scope, event.Attempt, event.MaxAttempts, event.Delay, event.Err)
	}
}

func (logger *searchProgressLogger) details(locationLabel string, event detailProgress) {
	switch event.Kind {
	case detailProgressStart:
		logger.printf("%s: %s start: %d network request(s), %d cached", locationLabel, event.Stage, event.Total, event.Cached)
	case detailProgressItem:
		if event.Err != nil {
			logger.printf("%s: %s %d/%d fetched (%d skipped): %v", locationLabel, event.Stage, event.Completed, event.Total, event.Skipped, event.Err)
			return
		}
		logger.printf("%s: %s %d/%d fetched (%d skipped)", locationLabel, event.Stage, event.Completed, event.Total, event.Skipped)
	case detailProgressPaused:
		if event.PausedUntil.IsZero() {
			logger.printf("%s: %s paused after %v", locationLabel, event.Stage, event.Err)
			return
		}
		logger.printf("%s: %s paused until %s after %v", locationLabel, event.Stage, event.PausedUntil.Format(time.RFC3339), event.Err)
	case detailProgressDone:
		logger.printf("%s: %s done: %d fetched, %d skipped, %d cached", locationLabel, event.Stage, event.Fetched, event.Skipped, event.Cached)
	}
}

type searchPlanEstimate struct {
	Requests int
	Pacing   time.Duration
}

func estimateMultiLocationSearchPlan(locations []string, discovery bool, options zillow.DiscoveryOptions, overrides map[string]int, locationDelay time.Duration) searchPlanEstimate {
	if len(locations) == 0 {
		return searchPlanEstimate{}
	}
	estimate := searchPlanEstimate{}
	for _, location := range locations {
		if !discovery {
			estimate.Requests++
			continue
		}
		locationOptions := applyLocationPageOverride(options, location, overrides)
		searchRequests := 0
		for _, route := range locationOptions.Routes {
			pages := locationOptions.MaxPages
			if route.MaxPages > 0 {
				pages = route.MaxPages
			}
			searchRequests += pages
		}
		estimate.Requests += searchRequests + 1 // rendered bootstrap page
		if searchRequests > 1 {
			estimate.Pacing += time.Duration(searchRequests-1) * locationOptions.PageDelay
		}
	}
	if len(locations) > 1 {
		estimate.Pacing += time.Duration(len(locations)-1) * locationDelay
	}
	return estimate
}
