package cli

import (
	"bytes"
	"fmt"
	"strings"
	"testing"
	"time"

	"gozillo/internal/zillow"
)

func TestEstimateMultiLocationSearchPlanUsesPerLocationPageCaps(t *testing.T) {
	t.Parallel()

	options := zillow.DiscoveryOptions{
		MaxPages:  3,
		PageDelay: 5 * time.Second,
		Routes: []zillow.SearchRoute{
			{Name: "newest", MaxPages: 3},
			{Name: "updated", MaxPages: 1},
		},
	}
	estimate := estimateMultiLocationSearchPlan(
		[]string{"Priority CA", "Secondary CA"},
		true,
		options,
		map[string]int{"secondary ca": 2},
		5*time.Second,
	)
	if estimate.Requests != 9 {
		t.Fatalf("requests = %d, want 9", estimate.Requests)
	}
	if estimate.Pacing != 30*time.Second {
		t.Fatalf("pacing = %s, want 30s", estimate.Pacing)
	}
}

func TestEstimateBayAreaExampleSearchPlan(t *testing.T) {
	t.Parallel()

	locations := make([]string, 61)
	overrides := make(map[string]int, 56)
	for index := range locations {
		locations[index] = fmt.Sprintf("location-%d", index+1)
		if index >= 5 {
			overrides[locations[index]] = 2
		}
	}
	options := zillow.DiscoveryOptions{
		MaxPages:  3,
		PageDelay: 5 * time.Second,
		Routes: []zillow.SearchRoute{
			{Name: "beds-2-days", MaxPages: 3},
			{Name: "beds-2-updated", MaxPages: 1},
			{Name: "beds-3plus-days", MaxPages: 3},
			{Name: "beds-3plus-updated", MaxPages: 1},
			{Name: "beds-2-supplemental", MaxPages: 1},
			{Name: "beds-3plus-supplemental", MaxPages: 1},
			{Name: "keyword-1", MaxPages: 1},
			{Name: "keyword-2", MaxPages: 1},
			{Name: "keyword-3", MaxPages: 1},
			{Name: "keyword-4", MaxPages: 1},
			{Name: "keyword-5", MaxPages: 1},
			{Name: "keyword-6", MaxPages: 1},
			{Name: "keyword-7", MaxPages: 1},
			{Name: "keyword-8", MaxPages: 1},
		},
	}
	estimate := estimateMultiLocationSearchPlan(locations, true, options, overrides, 5*time.Second)
	if estimate.Requests != 1047 {
		t.Fatalf("requests = %d, want 1047", estimate.Requests)
	}
	if estimate.Pacing != 82*time.Minute+5*time.Second {
		t.Fatalf("pacing = %s, want 1h22m5s", estimate.Pacing)
	}
}

func TestSearchProgressLoggerFormatsDiscoveryAndDetailEvents(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	logger := newSearchProgressLogger(true, &buffer)
	logger.discovery("location 1/2 \"94501\"", zillow.DiscoveryProgress{
		Stage:      zillow.DiscoveryProgressPage,
		Route:      "beds-2-sort-days",
		RouteIndex: 1,
		RouteCount: 14,
		Page:       2,
		PageLimit:  3,
		Delay:      5 * time.Second,
	})
	logger.details("location 1/2 \"94501\"", detailProgress{
		Stage: detailProgressRecency,
		Kind:  detailProgressDone,
		Total: 10, Fetched: 4, Skipped: 6, Cached: 2,
	})

	got := buffer.String()
	for _, want := range []string{
		"progress +",
		"waiting 5s, then route 1/14 \"beds-2-sort-days\" page 2/3",
		"recency details done: 4 fetched, 6 skipped, 2 cached",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("progress output %q missing %q", got, want)
		}
	}
}

func TestSearchProgressLoggerCanBeDisabled(t *testing.T) {
	t.Parallel()

	var buffer bytes.Buffer
	logger := newSearchProgressLogger(false, &buffer)
	logger.printf("hidden")
	if buffer.Len() != 0 {
		t.Fatalf("disabled progress output = %q", buffer.String())
	}
}
