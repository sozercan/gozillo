package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"gozillo/internal/zillow"
)

func TestFilterDetailedListings(t *testing.T) {
	t.Parallel()

	price := int64(4500)
	sqft := int64(1400)
	days := int64(3)
	listings := []zillow.Listing{
		{
			ID:              "match",
			Price:           &price,
			LivingArea:      &sqft,
			DaysOnZillow:    &days,
			Availability:    "2026-08-01",
			Laundry:         zillow.LaundryInUnit,
			Parking:         zillow.ParkingPrivateGarage,
			PetPolicy:       zillow.PetPolicyRestricted,
			AllowedPets:     []string{"Cats allowed"},
			FlexSpaces:      []string{"office"},
			DetailStatus:    zillow.DetailStatusEnriched,
			LaundryFeatures: []string{"In Unit"},
		},
		{
			ID:           "too-old",
			LivingArea:   &sqft,
			DaysOnZillow: int64TestPointer(30),
			Availability: "2026-08-01",
			Laundry:      zillow.LaundryInUnit,
			Parking:      zillow.ParkingPrivateGarage,
			PetPolicy:    zillow.PetPolicyAllowed,
			AllowedPets:  []string{"Pets allowed"},
			FlexSpaces:   []string{"office"},
		},
	}
	from := dateForTest(t, "2026-07-20")
	by := dateForTest(t, "2026-08-31")
	options := detailFilterOptions{
		Workers:         2,
		MinSqft:         1200,
		MaxDaysOnZillow: 10,
		AvailableFrom:   &from,
		AvailableBy:     &by,
		Laundry:         zillow.LaundryInUnit,
		Parking:         zillow.ParkingPrivateGarage,
		Pets:            "cats",
		Flex:            []string{"den", "office"},
		UnknownLaundry:  unknownLaundryExclude,
	}

	got := filterDetailedListings(listings, options, time.Date(2026, time.July, 19, 12, 0, 0, 0, time.UTC))
	if len(got) != 1 || got[0].ID != "match" {
		t.Fatalf("filtered listings = %+v", got)
	}
	if got[0].MatchStatus != zillow.MatchStatusMatch {
		t.Fatalf("MatchStatus = %q", got[0].MatchStatus)
	}
}

func TestFilterDetailedListingsCanRetainUnknownLaundryAsWatchlist(t *testing.T) {
	t.Parallel()

	listings := []zillow.Listing{
		{ID: "unknown", Laundry: zillow.LaundryUnknown, DetailStatus: zillow.DetailStatusUnavailable},
		{ID: "hookups", Laundry: zillow.LaundryHookups, DetailStatus: zillow.DetailStatusEnriched},
	}
	options := detailFilterOptions{
		Workers:         2,
		MaxDaysOnZillow: -1,
		Laundry:         zillow.LaundryInUnit,
		Parking:         filterAny,
		Pets:            filterAny,
		UnknownLaundry:  unknownLaundryWatch,
	}

	got := filterDetailedListings(listings, options, time.Date(2026, time.July, 19, 0, 0, 0, 0, time.UTC))
	if len(got) != 1 || got[0].ID != "unknown" {
		t.Fatalf("filtered listings = %+v", got)
	}
	if got[0].MatchStatus != zillow.MatchStatusWatchlist || !reflect.DeepEqual(got[0].MatchReasons, []string{"laundry details are unknown"}) {
		t.Fatalf("watchlist metadata = (%q, %#v)", got[0].MatchStatus, got[0].MatchReasons)
	}
}

func TestEnrichRecencyAfterDetailFiltersFetchesOnlySurvivors(t *testing.T) {
	t.Parallel()

	days := int64(2)
	keepURL := "https://www.zillow.com/homedetails/keep_zpid/"
	dropURL := "https://www.zillow.com/homedetails/drop_zpid/"
	fetcher := &fakePropertyFetcher{
		calls: map[string]int{},
		property: &zillow.Property{
			ID:           "keep",
			URL:          keepURL,
			DaysOnZillow: &days,
		},
	}
	enricher := newListingDetailEnricher(fetcher, 1, 0, nil)
	options := detailFilterOptions{
		VerifyRecency:   true,
		Workers:         1,
		MaxDaysOnZillow: -1,
		Laundry:         zillow.LaundryInUnit,
		Parking:         filterAny,
		Pets:            filterAny,
		UnknownLaundry:  unknownLaundryExclude,
	}
	listings := []zillow.Listing{
		{ID: "keep", URL: keepURL, Laundry: zillow.LaundryInUnit},
		{ID: "drop", URL: dropURL, Laundry: zillow.LaundryNone},
	}

	got := enrichRecencyAfterDetailFilters(context.Background(), enricher, listings, options, time.Now())
	if len(got) != 1 || got[0].ID != "keep" || got[0].DaysOnZillow == nil || *got[0].DaysOnZillow != days {
		t.Fatalf("recency-enriched listings = %+v", got)
	}
	if fetcher.calls[keepURL] != 1 || fetcher.calls[dropURL] != 0 {
		t.Fatalf("fetch calls = %#v", fetcher.calls)
	}
}

func TestMaxDaysOnZillowRunsAfterRecencyBackfill(t *testing.T) {
	t.Parallel()

	freshDays := int64(2)
	staleDays := int64(20)
	freshURL := "https://www.zillow.com/homedetails/fresh_zpid/"
	staleURL := "https://www.zillow.com/homedetails/stale_zpid/"
	fetcher := &sequenceRentalPageFetcher{results: []detailFetchResult{
		{page: &zillow.RentalPage{Kind: zillow.RentalPageProperty, Properties: []zillow.Property{{URL: freshURL, DaysOnZillow: &freshDays}}}},
		{page: &zillow.RentalPage{Kind: zillow.RentalPageProperty, Properties: []zillow.Property{{URL: staleURL, DaysOnZillow: &staleDays}}}},
	}}
	enricher := newListingDetailEnricher(fetcher, 1, 0, nil)
	options := detailFilterOptions{
		VerifyRecency:   true,
		Workers:         1,
		MaxDaysOnZillow: 10,
		Laundry:         filterAny,
		Parking:         filterAny,
		Pets:            filterAny,
		UnknownLaundry:  unknownLaundryExclude,
	}
	listings := []zillow.Listing{{ID: "fresh", URL: freshURL}, {ID: "stale", URL: staleURL}}

	got := enrichRecencyAfterDetailFilters(context.Background(), enricher, listings, options, time.Now())
	if fetcher.calls != 2 || len(got) != 1 || got[0].ID != "fresh" {
		t.Fatalf("calls = %d, listings = %+v", fetcher.calls, got)
	}
}

func TestWatchlistReasonsSurviveRecencyPhase(t *testing.T) {
	t.Parallel()

	days := int64(2)
	url := "https://www.zillow.com/homedetails/watchlist_zpid/"
	fetcher := &sequenceRentalPageFetcher{results: []detailFetchResult{{
		page: &zillow.RentalPage{Kind: zillow.RentalPageProperty, Properties: []zillow.Property{{URL: url, DaysOnZillow: &days}}},
	}}}
	enricher := newListingDetailEnricher(fetcher, 1, 0, nil)
	availableBy := dateForTest(t, "2026-08-31")
	options := detailFilterOptions{
		VerifyRecency:       true,
		Workers:             1,
		MaxDaysOnZillow:     10,
		AvailableBy:         &availableBy,
		UnknownAvailability: availabilityWatchlist,
		MaxTotalCost:        5500,
		Laundry:             zillow.LaundryInUnit,
		Parking:             filterAny,
		Pets:                filterAny,
		UnknownLaundry:      unknownLaundryWatch,
	}

	got := enrichRecencyAfterDetailFilters(context.Background(), enricher, []zillow.Listing{{ID: "watchlist", URL: url}}, options, time.Now())
	if len(got) != 1 || got[0].MatchStatus != zillow.MatchStatusWatchlist {
		t.Fatalf("listings = %+v", got)
	}
	for _, reason := range []string{"availability needs verification", "total monthly cost needs verification", "laundry details are unknown"} {
		if !slices.Contains(got[0].MatchReasons, reason) {
			t.Fatalf("reasons = %#v, missing %q", got[0].MatchReasons, reason)
		}
	}
}

func TestStrictBoundaryFiltersExpandedUnitsBeforeRecency(t *testing.T) {
	t.Parallel()

	days := int64(2)
	keepURL := "https://www.zillow.com/homedetails/in-boundary_zpid/"
	dropURL := "https://www.zillow.com/homedetails/out-of-boundary_zpid/"
	fetcher := &sequenceRentalPageFetcher{results: []detailFetchResult{{
		page: &zillow.RentalPage{Kind: zillow.RentalPageProperty, Properties: []zillow.Property{{URL: keepURL, DaysOnZillow: &days}}},
	}}}
	enricher := newListingDetailEnricher(fetcher, 1, 0, nil)
	options := detailFilterOptions{
		VerifyRecency:   true,
		Workers:         1,
		MaxDaysOnZillow: -1,
		Laundry:         filterAny,
		Parking:         filterAny,
		Pets:            filterAny,
		UnknownLaundry:  unknownLaundryExclude,
	}
	listings := []zillow.Listing{
		{ID: "keep", URL: keepURL, Address: zillow.Address{PostalCode: "94501"}},
		{ID: "drop", URL: dropURL, Address: zillow.Address{PostalCode: "94601"}},
	}
	afterDetails := func(listings []zillow.Listing) []zillow.Listing {
		return filterListingsByLocationBoundary(listings, "94501", locationBoundaryOptions{Strict: true}, false)
	}

	got := finalizeEnrichedListings(context.Background(), enricher, listings, options, time.Now(), afterDetails)
	if len(got) != 1 || got[0].ID != "keep" || fetcher.calls != 1 || len(fetcher.urls) != 1 || fetcher.urls[0] != keepURL {
		t.Fatalf("listings = %+v, calls = %d, urls = %v", got, fetcher.calls, fetcher.urls)
	}
}

func TestNewestSortUsesRecencyBackfill(t *testing.T) {
	t.Parallel()

	olderDays := int64(10)
	newerDays := int64(1)
	olderURL := "https://www.zillow.com/homedetails/older_zpid/"
	newerURL := "https://www.zillow.com/homedetails/newer_zpid/"
	fetcher := &sequenceRentalPageFetcher{results: []detailFetchResult{
		{page: &zillow.RentalPage{Kind: zillow.RentalPageProperty, Properties: []zillow.Property{{URL: olderURL, DaysOnZillow: &olderDays}}}},
		{page: &zillow.RentalPage{Kind: zillow.RentalPageProperty, Properties: []zillow.Property{{URL: newerURL, DaysOnZillow: &newerDays}}}},
	}}
	enricher := newListingDetailEnricher(fetcher, 1, 0, nil)
	options := detailFilterOptions{
		VerifyRecency:   true,
		Workers:         1,
		MaxDaysOnZillow: -1,
		Laundry:         filterAny,
		Parking:         filterAny,
		Pets:            filterAny,
		UnknownLaundry:  unknownLaundryExclude,
	}
	listings := []zillow.Listing{{ID: "older", URL: olderURL}, {ID: "newer", URL: newerURL}}

	got := enrichRecencyAfterDetailFilters(context.Background(), enricher, listings, options, time.Now())
	sortListings(got, sortNewest)
	if len(got) != 2 || got[0].ID != "newer" || got[1].ID != "older" {
		t.Fatalf("newest order = %+v", got)
	}
}

func TestValidateDetailFilterOptions(t *testing.T) {
	t.Parallel()

	valid := detailFilterOptions{
		Workers:         2,
		MaxDaysOnZillow: -1,
		Laundry:         filterAny,
		Parking:         filterAny,
		Pets:            filterAny,
		UnknownLaundry:  unknownLaundryExclude,
	}
	if err := validateDetailFilterOptions(valid); err != nil {
		t.Fatalf("valid options error = %v", err)
	}

	tests := []detailFilterOptions{
		func() detailFilterOptions { value := valid; value.Workers = 0; return value }(),
		func() detailFilterOptions { value := valid; value.Laundry = "washer"; return value }(),
		func() detailFilterOptions { value := valid; value.Parking = "driveway"; return value }(),
		func() detailFilterOptions { value := valid; value.Pets = "maybe"; return value }(),
		func() detailFilterOptions { value := valid; value.Flex = []string{"living-room"}; return value }(),
		func() detailFilterOptions { value := valid; value.UnknownLaundry = unknownLaundryWatch; return value }(),
	}
	for index, options := range tests {
		if err := validateDetailFilterOptions(options); err == nil {
			t.Fatalf("case %d: error = nil", index)
		}
	}
}

func TestMaxTotalCostRequiresDetailEnrichment(t *testing.T) {
	t.Parallel()

	options := detailFilterOptions{
		MaxDaysOnZillow: -1,
		MaxTotalCost:    3500,
		Laundry:         filterAny,
		Parking:         filterAny,
		Pets:            filterAny,
		UnknownLaundry:  unknownLaundryExclude,
	}
	if !options.needsEnrichment() || !options.requiresAmenityDetails() {
		t.Fatal("max-total-cost did not trigger detail enrichment")
	}
}

func TestPrefilterDefersBuildingMetadataUntilCommunityExpansion(t *testing.T) {
	t.Parallel()

	oldDays := int64(90)
	highCost := int64(9000)
	building := zillow.Listing{
		ID:               "building",
		IsBuilding:       true,
		DaysOnZillow:     &oldDays,
		TotalMonthlyCost: &highCost,
		Availability:     "2027-01-01",
		Status:           "OFF_MARKET",
		SharedHousing:    true,
	}
	availableBy := dateForTest(t, "2026-08-01")
	options := detailFilterOptions{
		MaxDaysOnZillow:      10,
		AvailableBy:          &availableBy,
		MaxTotalCost:         5000,
		RequireForRent:       true,
		ExcludeSharedHousing: true,
		Laundry:              filterAny,
		Parking:              filterAny,
		Pets:                 filterAny,
		UnknownLaundry:       unknownLaundryExclude,
	}

	got := prefilterKnownListingMetadata([]zillow.Listing{building}, options, time.Date(2026, time.July, 21, 0, 0, 0, 0, time.UTC))
	if len(got) != 1 || got[0].ID != building.ID {
		t.Fatalf("building was removed before expansion: %+v", got)
	}
}

func TestPrefilterDefersKnownRecencyWhenVerificationEnabled(t *testing.T) {
	t.Parallel()

	days := int64(20)
	listing := zillow.Listing{ID: "stale-card", DaysOnZillow: &days}
	options := detailFilterOptions{
		VerifyRecency:   true,
		Workers:         1,
		MaxDaysOnZillow: 10,
		Laundry:         filterAny,
		Parking:         filterAny,
		Pets:            filterAny,
		UnknownLaundry:  unknownLaundryExclude,
	}
	if got := prefilterKnownListingMetadata([]zillow.Listing{listing}, options, time.Now()); len(got) != 1 {
		t.Fatalf("verified recency candidate was removed early: %+v", got)
	}
	options.VerifyRecency = false
	if got := prefilterKnownListingMetadata([]zillow.Listing{listing}, options, time.Now()); len(got) != 0 {
		t.Fatalf("unverified stale candidate survived prefilter: %+v", got)
	}
}

func TestPrepareListingsForDetailEnrichmentDefersLimitForRecency(t *testing.T) {
	t.Parallel()

	listings := []zillow.Listing{{ID: "first"}, {ID: "second"}}
	applyCalls := 0
	applyLimit := func(listings []zillow.Listing) []zillow.Listing {
		applyCalls++
		return listings[:1]
	}
	options := detailFilterOptions{
		VerifyRecency:   true,
		Workers:         1,
		MaxDaysOnZillow: -1,
		Laundry:         filterAny,
		Parking:         filterAny,
		Pets:            filterAny,
		UnknownLaundry:  unknownLaundryExclude,
	}
	got := prepareListingsForDetailEnrichment(listings, options, time.Now(), applyLimit)
	if len(got) != 2 || applyCalls != 0 {
		t.Fatalf("recency preparation = %+v, apply calls = %d", got, applyCalls)
	}
	options.VerifyRecency = false
	got = prepareListingsForDetailEnrichment(listings, options, time.Now(), applyLimit)
	if len(got) != 1 || applyCalls != 1 {
		t.Fatalf("non-recency preparation = %+v, apply calls = %d", got, applyCalls)
	}
}

func TestSearchCommandAppliesSqftFreshnessAndAvailabilityFiltersToSnapshot(t *testing.T) {
	t.Parallel()

	nextData := map[string]any{
		"props": map[string]any{
			"pageProps": map[string]any{
				"searchPageState": map[string]any{
					"queryState": map[string]any{"pagination": map[string]any{"currentPage": 1}},
					"cat1": map[string]any{
						"searchResults": map[string]any{
							"listResults": []any{
								map[string]any{"zpid": "1", "address": "1 Main St", "unformattedPrice": 4500, "beds": 3, "baths": 2, "area": 1300, "daysOnZillow": 2, "availabilityDate": "2026-08-01"},
								map[string]any{"zpid": "2", "address": "2 Pine St", "unformattedPrice": 4400, "beds": 3, "baths": 2, "area": 900, "daysOnZillow": 1, "availabilityDate": "2026-08-01"},
								map[string]any{"zpid": "3", "address": "3 Oak St", "unformattedPrice": 4300, "beds": 3, "baths": 2, "area": 1400, "daysOnZillow": 20, "availabilityDate": "2026-08-01"},
							},
						},
						"searchList": map[string]any{"totalResultCount": 3},
					},
				},
			},
		},
	}
	data, err := json.Marshal(nextData)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "search.next.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Execute([]string{
		"--output=json", "search", "--snapshot", path,
		"--min-sqft", "1200", "--max-days-on-zillow", "5",
		"--available-from", "2026-07-20", "--available-by", "2026-08-31",
	}, &stdout, &stderr)
	if code != ExitOK {
		t.Fatalf("search code = %d, stderr = %q", code, stderr.String())
	}
	var result zillow.SearchResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if len(result.Listings) != 1 || result.Listings[0].ID != "1" {
		t.Fatalf("filtered listings = %+v", result.Listings)
	}
	if result.Listings[0].MatchStatus != zillow.MatchStatusMatch {
		t.Fatalf("MatchStatus = %q", result.Listings[0].MatchStatus)
	}
}

func TestSearchCommandMarksUnfetchableLaundryAsWatchlist(t *testing.T) {
	t.Parallel()

	nextData := map[string]any{
		"props": map[string]any{
			"pageProps": map[string]any{
				"searchPageState": map[string]any{
					"cat1": map[string]any{
						"searchResults": map[string]any{"listResults": []any{map[string]any{"zpid": "1", "address": "1 Main St"}}},
					},
				},
			},
		},
	}
	data, _ := json.Marshal(nextData)
	path := filepath.Join(t.TempDir(), "search.next.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Execute([]string{
		"--output=json", "search", "--snapshot", path,
		"--laundry", "in-unit", "--unknown-laundry", "watchlist", "--tls-profile", "default",
	}, &stdout, &stderr)
	if code != ExitOK {
		t.Fatalf("search code = %d, stderr = %q", code, stderr.String())
	}
	var result zillow.SearchResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Listings) != 1 {
		t.Fatalf("listings = %+v", result.Listings)
	}
	listing := result.Listings[0]
	if listing.DetailStatus != zillow.DetailStatusUnavailable || listing.Laundry != zillow.LaundryUnknown || listing.MatchStatus != zillow.MatchStatusWatchlist {
		t.Fatalf("listing = %+v", listing)
	}
	if !strings.Contains(listing.DetailError, "no property URL") {
		t.Fatalf("DetailError = %q", listing.DetailError)
	}
}

func TestMergePropertyDetails(t *testing.T) {
	t.Parallel()

	listing := zillow.Listing{ID: "1"}
	property := zillow.Property{
		YearBuilt:       int64TestPointer(2015),
		LivingArea:      int64TestPointer(1500),
		DaysOnZillow:    int64TestPointer(2),
		Availability:    "2026-08-01",
		Description:     "Dedicated office",
		Laundry:         zillow.LaundryInUnit,
		LaundryFeatures: []string{"In Unit"},
		Parking:         zillow.ParkingPrivateGarage,
		ParkingFeatures: []string{"Attached Garage"},
		PetPolicy:       zillow.PetPolicyAllowed,
		AllowedPets:     []string{"Pets allowed"},
		FlexSpaces:      []string{"office", zillow.ParkingPrivateGarage},
	}
	mergePropertyDetails(&listing, &property)
	if listing.DetailStatus != zillow.DetailStatusEnriched || listing.LivingArea == nil || *listing.LivingArea != 1500 {
		t.Fatalf("listing = %+v", listing)
	}
	if !reflect.DeepEqual(listing.FlexSpaces, property.FlexSpaces) {
		t.Fatalf("FlexSpaces = %#v", listing.FlexSpaces)
	}
}

func int64TestPointer(value int64) *int64 { return &value }

func dateForTest(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

type fakePropertyFetcher struct {
	mu       sync.Mutex
	calls    map[string]int
	property *zillow.Property
	err      error
}

func (fetcher *fakePropertyFetcher) FetchRentalPage(_ context.Context, url string) (*zillow.RentalPage, error) {
	fetcher.mu.Lock()
	defer fetcher.mu.Unlock()
	fetcher.calls[url]++
	if fetcher.err != nil {
		return nil, fetcher.err
	}
	if fetcher.property == nil {
		return &zillow.RentalPage{Kind: zillow.RentalPageProperty}, nil
	}
	return &zillow.RentalPage{Kind: zillow.RentalPageProperty, Properties: []zillow.Property{*fetcher.property}}, nil
}

func TestListingDetailEnricherDeduplicatesAndCachesURLs(t *testing.T) {
	t.Parallel()

	fetcher := &fakePropertyFetcher{
		calls: map[string]int{},
		property: &zillow.Property{
			Laundry: zillow.LaundryInUnit,
			Parking: zillow.ParkingGarage,
		},
	}
	enricher := newListingDetailEnricher(fetcher, 2, 0, nil)
	url := "https://www.zillow.com/homedetails/example/123_zpid/"
	first := []zillow.Listing{{ID: "1", URL: url}, {ID: "1-copy", URL: url}}
	enricher.Enrich(context.Background(), first)
	second := []zillow.Listing{{ID: "1-again", URL: url}}
	enricher.Enrich(context.Background(), second)

	fetcher.mu.Lock()
	calls := fetcher.calls[url]
	fetcher.mu.Unlock()
	if calls != 1 {
		t.Fatalf("FetchProperty calls = %d, want 1", calls)
	}
	for _, listing := range append(first, second...) {
		if listing.DetailStatus != zillow.DetailStatusEnriched || listing.Laundry != zillow.LaundryInUnit {
			t.Fatalf("listing = %+v", listing)
		}
	}
}

func TestListingDetailEnricherKeepsGlobalStartPacingWithTwoWorkers(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	var waits []time.Duration
	enricher := newListingDetailEnricher(nil, 2, 5*time.Second, nil)
	enricher.paceNow = func() time.Time { return now }
	enricher.paceWait = func(_ context.Context, delay time.Duration) error {
		waits = append(waits, delay)
		now = now.Add(delay)
		return nil
	}

	if err := enricher.waitForTurn(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := enricher.waitForTurn(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(waits) != 2 || waits[0] != 0 || waits[1] != 5*time.Second {
		t.Fatalf("waits = %v, want [0s 5s]", waits)
	}
}

func TestListingDetailEnricherPausesRemainingRequestsAfterChallenge(t *testing.T) {
	t.Parallel()

	fetcher := &fakePropertyFetcher{
		calls: map[string]int{},
		err:   &zillow.ChallengeError{StatusCode: http.StatusForbidden, Reason: "test"},
	}
	enricher := newListingDetailEnricher(fetcher, 1, 0, nil)
	listings := []zillow.Listing{
		{ID: "1", URL: "https://www.zillow.com/homedetails/1_zpid/"},
		{ID: "2", URL: "https://www.zillow.com/homedetails/2_zpid/"},
		{ID: "3", URL: "https://www.zillow.com/homedetails/3_zpid/"},
	}
	var progress []detailProgress
	enricher.setProgress(func(event detailProgress) {
		progress = append(progress, event)
	})

	got := enricher.Enrich(context.Background(), listings)
	totalCalls := 0
	for _, calls := range fetcher.calls {
		totalCalls += calls
	}
	if totalCalls != 1 {
		t.Fatalf("FetchRentalPage calls = %d, want 1 after challenge", totalCalls)
	}
	for _, listing := range got {
		if listing.DetailStatus != zillow.DetailStatusUnavailable {
			t.Fatalf("listing = %+v, want unavailable details", listing)
		}
	}
	var paused, done *detailProgress
	for index := range progress {
		switch progress[index].Kind {
		case detailProgressPaused:
			paused = &progress[index]
		case detailProgressDone:
			done = &progress[index]
		}
	}
	if paused == nil || paused.PausedUntil.IsZero() || paused.Err == nil {
		t.Fatalf("paused progress = %+v", paused)
	}
	if done == nil || done.Fetched != 1 || done.Skipped != 2 || done.Total != 3 {
		t.Fatalf("done progress = %+v", done)
	}
}

func TestListingDetailEnricherStopsRecencyPassAfterChallenge(t *testing.T) {
	t.Parallel()

	fetcher := &sequenceRentalPageFetcher{results: []detailFetchResult{
		{err: &zillow.ChallengeError{StatusCode: http.StatusForbidden, Reason: "test"}},
		{page: &zillow.RentalPage{Kind: zillow.RentalPageProperty}},
	}}
	enricher := newListingDetailEnricher(fetcher, 1, 0, nil)
	listings := []zillow.Listing{
		{ID: "1", URL: "https://www.zillow.com/homedetails/unit-1_zpid/"},
		{ID: "2", URL: "https://www.zillow.com/homedetails/unit-2_zpid/"},
		{ID: "3", URL: "https://www.zillow.com/homedetails/unit-3_zpid/"},
	}
	var progress []detailProgress
	enricher.setProgress(func(event detailProgress) {
		progress = append(progress, event)
	})

	enricher.EnrichRecency(context.Background(), listings)
	if fetcher.calls != 1 {
		t.Fatalf("FetchRentalPage calls = %d, want 1 after challenge", fetcher.calls)
	}
	pausedEvents := 0
	var done *detailProgress
	for index := range progress {
		switch progress[index].Kind {
		case detailProgressPaused:
			pausedEvents++
		case detailProgressDone:
			done = &progress[index]
		}
	}
	if pausedEvents != 1 {
		t.Fatalf("paused events = %d, progress = %+v", pausedEvents, progress)
	}
	if done == nil || done.Fetched != 1 || done.Skipped != 2 || done.Total != 3 {
		t.Fatalf("done progress = %+v", done)
	}

	enricher.resetPause()
	enricher.Enrich(context.Background(), []zillow.Listing{
		{ID: "shared-challenge", URL: listings[0].URL},
		{ID: "next-location", URL: "https://www.zillow.com/homedetails/next-location_zpid/"},
	})
	if fetcher.calls != 2 {
		t.Fatalf("next location FetchRentalPage calls = %d, want 2 after resetting pause", fetcher.calls)
	}
}

func TestListingDetailEnricherRecencyRetryExpiresChallenge(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	url := "https://www.zillow.com/homedetails/example/recency-retry_zpid/"
	days := int64(7)
	fetcher := &sequenceRentalPageFetcher{
		results: []detailFetchResult{
			{err: &zillow.ChallengeError{URL: url, StatusCode: http.StatusForbidden}},
			{page: &zillow.RentalPage{Kind: zillow.RentalPageProperty, Properties: []zillow.Property{{ID: "recency-retry", URL: url, DaysOnZillow: &days}}}},
		},
	}
	cache := &diskCache{Dir: filepath.Join(t.TempDir(), "recency-retry-cache"), TTL: time.Hour, Now: func() time.Time { return now }}
	enricher := newListingDetailEnricher(fetcher, 1, 0, cache)
	listing := []zillow.Listing{{ID: "recency-retry", URL: url}}

	first := enricher.EnrichRecency(context.Background(), listing)
	if first[0].DaysOnZillow != nil || fetcher.calls != 1 {
		t.Fatalf("first recency pass = %+v, calls = %d", first, fetcher.calls)
	}
	now = now.Add(6 * time.Minute)
	second := enricher.EnrichRecency(context.Background(), listing)
	if second[0].DaysOnZillow == nil || *second[0].DaysOnZillow != days || fetcher.calls != 2 {
		t.Fatalf("second recency pass = %+v, calls = %d", second, fetcher.calls)
	}
}

func TestDetailRateLimitUsesServerRetryAfter(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	cache := &diskCache{Now: func() time.Time { return now }}
	enricher := newListingDetailEnricher(nil, 1, 0, cache)
	result := enricher.newDetailFetchResult(nil, &zillow.RateLimitError{RetryAfter: 90 * time.Second})
	if !result.retryAfter.Equal(now.Add(90 * time.Second)) {
		t.Fatalf("retryAfter = %s, want %s", result.retryAfter, now.Add(90*time.Second))
	}
}

func TestPersistentRetryableDetailErrorPreservesIdentity(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	cache := &diskCache{Dir: filepath.Join(t.TempDir(), "retryable-cache"), TTL: time.Hour, Now: func() time.Time { return now }}
	url := "https://www.zillow.com/homedetails/persisted-challenge_zpid/"
	first := newListingDetailEnricher(nil, 1, 0, cache)
	first.savePersistent(url, first.newDetailFetchResult(nil, &zillow.ChallengeError{URL: url, StatusCode: http.StatusForbidden}))

	second := newListingDetailEnricher(nil, 1, 0, cache)
	result, hit := second.loadPersistent(url)
	if !hit || result.err == nil || !errors.Is(result.err, zillow.ErrChallenge) {
		t.Fatalf("persistent result = %+v, hit = %t", result, hit)
	}
	if !result.retryAfter.Equal(now.Add(5 * time.Minute)) {
		t.Fatalf("retryAfter = %s", result.retryAfter)
	}
}

type sequenceRentalPageFetcher struct {
	mu      sync.Mutex
	results []detailFetchResult
	calls   int
	urls    []string
}

func (fetcher *sequenceRentalPageFetcher) FetchRentalPage(_ context.Context, url string) (*zillow.RentalPage, error) {
	fetcher.mu.Lock()
	defer fetcher.mu.Unlock()
	index := fetcher.calls
	fetcher.calls++
	fetcher.urls = append(fetcher.urls, url)
	if index >= len(fetcher.results) {
		index = len(fetcher.results) - 1
	}
	return fetcher.results[index].page, fetcher.results[index].err
}

func TestListingDetailEnricherRetriesExpiredChallengeFromMemory(t *testing.T) {
	t.Parallel()

	url := "https://www.zillow.com/homedetails/example/expired_zpid/"
	fetcher := &fakePropertyFetcher{
		calls:    map[string]int{},
		property: &zillow.Property{ID: "expired", Laundry: zillow.LaundryInUnit},
	}
	enricher := newListingDetailEnricher(fetcher, 1, 0, nil)
	enricher.store(url, detailFetchResult{
		err:        &zillow.ChallengeError{URL: url, StatusCode: http.StatusForbidden},
		retryAfter: time.Now().Add(-time.Minute),
	})

	got := enricher.Enrich(context.Background(), []zillow.Listing{{ID: "expired", URL: url}})
	if fetcher.calls[url] != 1 {
		t.Fatalf("fetch calls = %d, want 1", fetcher.calls[url])
	}
	if len(got) != 1 || got[0].DetailStatus != zillow.DetailStatusEnriched || got[0].Laundry != zillow.LaundryInUnit {
		t.Fatalf("enriched listing = %+v", got)
	}
}

func TestListingDetailEnricherKeepsUnexpiredChallengeInMemory(t *testing.T) {
	t.Parallel()

	url := "https://www.zillow.com/homedetails/example/cooling-down_zpid/"
	fetcher := &fakePropertyFetcher{calls: map[string]int{}, property: &zillow.Property{ID: "cooling-down"}}
	enricher := newListingDetailEnricher(fetcher, 1, 0, nil)
	enricher.store(url, detailFetchResult{
		err:        &zillow.ChallengeError{URL: url, StatusCode: http.StatusForbidden},
		retryAfter: time.Now().Add(time.Minute),
	})

	got := enricher.Enrich(context.Background(), []zillow.Listing{{ID: "cooling-down", URL: url}})
	if fetcher.calls[url] != 0 {
		t.Fatalf("fetch calls = %d, want 0", fetcher.calls[url])
	}
	if len(got) != 1 || got[0].DetailStatus != zillow.DetailStatusUnavailable {
		t.Fatalf("cached challenge listing = %+v", got)
	}
}

func TestSearchHelpListsDetailFilters(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := Execute([]string{"search", "--help"}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("Execute(search --help) code = %d, stderr = %q", code, stderr.String())
	}
	for _, flag := range []string{
		"--min-sqft", "--max-days-on-zillow", "--laundry", "--parking", "--pets", "--flex", "--enrich-details", "--verify-recency",
		"--max-pages", "--bed-range", "--server-sort", "--home-type", "--location-max-pages",
		"--supplemental-no-laundry", "--supplemental-pages", "--keyword-route",
		"--strict-location-boundary", "--allowed-city", "--location-city-alias",
		"--exclude-shared-housing", "--exclude-student-housing", "--exclude-income-restricted",
		"--max-total-cost", "--unknown-availability", "--verify-rental-status", "--previous-results", "--progress",
	} {
		if !strings.Contains(stdout.String(), flag) {
			t.Fatalf("search help missing %s", flag)
		}
	}
}

func TestListingAvailabilityDateUsesSuppliedCalendarDateForAvailableNow(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 19, 23, 30, 0, 0, time.FixedZone("PDT", -7*60*60))
	got, ok := listingAvailabilityDate("Available Now", now)
	if !ok || got.Format("2006-01-02") != "2026-07-19" {
		t.Fatalf("listingAvailabilityDate() = (%v, %t)", got, ok)
	}
}

func TestListingDetailEnricherReusesPersistentCacheAcrossInstances(t *testing.T) {
	t.Parallel()

	cache := &diskCache{Dir: filepath.Join(t.TempDir(), "property-cache"), TTL: time.Hour}
	url := "https://www.zillow.com/homedetails/example/456_zpid/"
	firstFetcher := &fakePropertyFetcher{
		calls: map[string]int{},
		property: &zillow.Property{
			ID:      "456",
			Laundry: zillow.LaundryInUnit,
		},
	}
	first := []zillow.Listing{{ID: "456", URL: url}}
	newListingDetailEnricher(firstFetcher, 1, 0, cache).Enrich(context.Background(), first)
	if firstFetcher.calls[url] != 1 || first[0].DetailStatus != zillow.DetailStatusEnriched {
		t.Fatalf("first enrichment = %+v, calls = %v", first, firstFetcher.calls)
	}

	secondFetcher := &fakePropertyFetcher{calls: map[string]int{}, err: errors.New("network should not run")}
	second := []zillow.Listing{{ID: "456", URL: url}}
	newListingDetailEnricher(secondFetcher, 1, 0, cache).Enrich(context.Background(), second)
	if secondFetcher.calls[url] != 0 {
		t.Fatalf("second fetch calls = %d, want 0", secondFetcher.calls[url])
	}
	if second[0].DetailStatus != zillow.DetailStatusEnriched || second[0].Laundry != zillow.LaundryInUnit {
		t.Fatalf("second enrichment = %+v", second)
	}
}

func TestListingDetailEnricherCachesSchemaFailuresAcrossInstances(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 20, 2, 0, 0, 0, time.UTC)
	cache := &diskCache{
		Dir: filepath.Join(t.TempDir(), "property-error-cache"),
		TTL: time.Hour,
		Now: func() time.Time { return now },
	}
	url := "https://www.zillow.com/apartments/example/ABC/"
	firstFetcher := &fakePropertyFetcher{
		calls: map[string]int{},
		err:   &zillow.SchemaDriftError{Operation: "property", Path: "gdpClientCache", Detail: "missing"},
	}
	first := []zillow.Listing{{ID: "building", URL: url}}
	newListingDetailEnricher(firstFetcher, 1, 0, cache).Enrich(context.Background(), first)
	if firstFetcher.calls[url] != 1 || first[0].DetailStatus != zillow.DetailStatusUnavailable {
		t.Fatalf("first result = %+v, calls = %v", first, firstFetcher.calls)
	}

	secondFetcher := &fakePropertyFetcher{calls: map[string]int{}, err: errors.New("network should not run")}
	second := []zillow.Listing{{ID: "building", URL: url}}
	newListingDetailEnricher(secondFetcher, 1, 0, cache).Enrich(context.Background(), second)
	if secondFetcher.calls[url] != 0 {
		t.Fatalf("second fetch calls = %d, want 0", secondFetcher.calls[url])
	}
	if second[0].DetailStatus != zillow.DetailStatusUnavailable || !strings.Contains(second[0].DetailError, "gdpClientCache") {
		t.Fatalf("second result = %+v", second)
	}
}

func TestParseLocationsSupportsStateQualifiedCommaLists(t *testing.T) {
	t.Parallel()

	got, err := parseLocations([]string{"Belmont CA,Richmond CA"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"Belmont CA", "Richmond CA"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseLocations() = %#v, want %#v", got, want)
	}
	for index, location := range got {
		url, err := locationSearchURL(location, true)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(url, "-CA/") {
			t.Fatalf("location %d URL = %q, want explicit CA", index, url)
		}
	}
}

func TestStateQualifiedLocationsRejectWrongRegionListings(t *testing.T) {
	t.Parallel()

	if got := locationStateQualifier("Belmont CA"); got != "CA" {
		t.Fatalf("locationStateQualifier() = %q", got)
	}
	if got := locationStateQualifier("94002"); got != "" {
		t.Fatalf("ZIP state qualifier = %q", got)
	}
	listings := []zillow.Listing{
		{ID: "ca", Address: zillow.Address{State: "CA"}},
		{ID: "ma", Address: zillow.Address{State: "MA"}},
	}
	filtered := filterListingsByState(listings, "CA")
	if len(filtered) != 1 || filtered[0].ID != "ca" {
		t.Fatalf("filterListingsByState() = %+v", filtered)
	}
}

func TestListingDetailEnricherExpandsCommunityUnits(t *testing.T) {
	t.Parallel()

	priceA := int64(4400)
	priceB := int64(5100)
	fetcher := &fakeRentalPageFetcher{
		calls: map[string]int{},
		page: &zillow.RentalPage{
			Kind: zillow.RentalPageCommunity,
			Properties: []zillow.Property{
				{ID: "unit-a", URL: "https://www.zillow.com/homedetails/100_zpid/", Price: &priceA, Laundry: zillow.LaundryInUnit, HomeType: "APARTMENT", Status: "FOR_RENT"},
				{ID: "unit-b", URL: "https://www.zillow.com/homedetails/200_zpid/", Price: &priceB, Laundry: zillow.LaundryInUnit, HomeType: "APARTMENT", Status: "FOR_RENT"},
			},
		},
	}
	url := "https://www.zillow.com/apartments/example/ABC/"
	enricher := newListingDetailEnricher(fetcher, 1, 0, nil)
	got := enricher.Enrich(context.Background(), []zillow.Listing{{ID: "building", URL: url, IsBuilding: true}})
	if len(got) != 2 {
		t.Fatalf("expanded listings = %d, want 2", len(got))
	}
	if got[0].ID != "unit-a" || got[1].ID != "unit-b" || got[0].Price == nil || *got[0].Price != 4400 || got[1].Price == nil || *got[1].Price != 5100 {
		t.Fatalf("expanded listings = %+v", got)
	}
	if got[0].IsBuilding || got[1].IsBuilding {
		t.Fatalf("expanded units retained building flag: %+v", got)
	}
	if fetcher.calls[url] != 1 {
		t.Fatalf("fetch calls = %d, want 1", fetcher.calls[url])
	}
}

func TestListingDetailEnricherCommunityChildrenUseUnmodifiedBuildingParent(t *testing.T) {
	t.Parallel()

	buildingPrice := int64(4200)
	firstUnitPrice := int64(4400)
	buildingBedrooms := 2.0
	firstUnitBedrooms := 1.0
	url := "https://www.zillow.com/apartments/example/ABC/"
	fetcher := &fakeRentalPageFetcher{
		calls: map[string]int{},
		page: &zillow.RentalPage{
			Kind: zillow.RentalPageCommunity,
			Properties: []zillow.Property{
				{
					ID: "unit-a", URL: "https://www.zillow.com/homedetails/100_zpid/",
					Price: &firstUnitPrice, Bedrooms: &firstUnitBedrooms, Availability: "2026-09-01",
				},
				{
					ID: "unit-b", URL: "https://www.zillow.com/homedetails/200_zpid/",
				},
			},
		},
	}
	parent := zillow.Listing{
		ID: "building", URL: url, IsBuilding: true,
		Price: &buildingPrice, Bedrooms: &buildingBedrooms, Availability: "2026-08-01",
	}

	got := newListingDetailEnricher(fetcher, 1, 0, nil).Enrich(context.Background(), []zillow.Listing{parent})
	if len(got) != 2 {
		t.Fatalf("expanded listings = %d, want 2", len(got))
	}
	later := got[1]
	if later.Price == nil || *later.Price != buildingPrice || later.Bedrooms == nil || *later.Bedrooms != buildingBedrooms || later.Availability != parent.Availability {
		t.Fatalf("later child inherited first child fields: %+v", later)
	}
}

func TestMergeCommunityPropertyDetailsClearsInheritedBuildingID(t *testing.T) {
	t.Parallel()

	price := int64(4500)
	listing := zillow.Listing{ID: "building-zpid", IsBuilding: true}
	mergeCommunityPropertyDetails(&listing, &zillow.Property{Price: &price})

	if listing.ID != "" {
		t.Fatalf("expanded child ID = %q, want no inherited building ID", listing.ID)
	}
	if listing.IsBuilding {
		t.Fatal("expanded child retained building flag")
	}
}

type fakeRentalPageFetcher struct {
	mu    sync.Mutex
	calls map[string]int
	page  *zillow.RentalPage
	err   error
}

func (fetcher *fakeRentalPageFetcher) FetchRentalPage(_ context.Context, url string) (*zillow.RentalPage, error) {
	fetcher.mu.Lock()
	defer fetcher.mu.Unlock()
	fetcher.calls[url]++
	return fetcher.page, fetcher.err
}

func TestFilterDetailedListingsUsesWatchlistsForAvailabilityAndUnknownTotalCost(t *testing.T) {
	t.Parallel()

	by := dateForTest(t, "2026-08-31")
	knownTotal := int64(5300)
	overTotal := int64(5700)
	beds := 3.0
	listings := []zillow.Listing{
		{ID: "target", Bedrooms: &beds, Availability: "2026-08-15", TotalMonthlyCost: &knownTotal, Status: "FOR_RENT", Laundry: zillow.LaundryInUnit},
		{ID: "unknown-availability", Bedrooms: &beds, Availability: "", TotalMonthlyCost: &knownTotal, Status: "FOR_RENT", Laundry: zillow.LaundryInUnit},
		{ID: "late", Bedrooms: &beds, Availability: "2026-09-15", TotalMonthlyCost: &knownTotal, Status: "FOR_RENT", Laundry: zillow.LaundryInUnit},
		{ID: "unknown-total", Bedrooms: &beds, Availability: "2026-08-10", Status: "FOR_RENT", Laundry: zillow.LaundryInUnit},
		{ID: "over-total", Bedrooms: &beds, Availability: "2026-08-10", TotalMonthlyCost: &overTotal, Status: "FOR_RENT", Laundry: zillow.LaundryInUnit},
		{ID: "off-market", Bedrooms: &beds, Availability: "2026-08-10", TotalMonthlyCost: &knownTotal, Status: "OFF_MARKET", Laundry: zillow.LaundryInUnit},
	}
	options := detailFilterOptions{
		Workers:                 1,
		MaxDaysOnZillow:         -1,
		AvailableBy:             &by,
		UnknownAvailability:     availabilityWatchlist,
		OutOfWindowAvailability: availabilityWatchlist,
		MaxTotalCost:            5500,
		RequireForRent:          true,
		Laundry:                 zillow.LaundryInUnit,
		Parking:                 filterAny,
		Pets:                    filterAny,
		UnknownLaundry:          unknownLaundryExclude,
	}

	got := filterDetailedListings(listings, options, time.Date(2026, time.July, 21, 0, 0, 0, 0, time.UTC))
	if ids := listingIDs(got); !reflect.DeepEqual(ids, []string{"target", "unknown-availability", "late", "unknown-total"}) {
		t.Fatalf("filtered IDs = %v", ids)
	}
	for _, listing := range got[1:] {
		if listing.MatchStatus != zillow.MatchStatusWatchlist || len(listing.MatchReasons) == 0 {
			t.Fatalf("watchlist listing = %+v", listing)
		}
	}
}

func TestTwoBedPrivateGarageOnlyIsVerificationWatchlist(t *testing.T) {
	t.Parallel()

	beds := 2.0
	listing := zillow.Listing{
		ID:         "garage-flex",
		Bedrooms:   &beds,
		Laundry:    zillow.LaundryInUnit,
		FlexSpaces: []string{zillow.ParkingPrivateGarage},
		Status:     "FOR_RENT",
	}
	options := detailFilterOptions{
		Workers:         1,
		MaxDaysOnZillow: -1,
		Laundry:         zillow.LaundryInUnit,
		Parking:         filterAny,
		Pets:            filterAny,
		UnknownLaundry:  unknownLaundryExclude,
		RequireForRent:  true,
	}
	got := filterDetailedListings([]zillow.Listing{listing}, options, time.Date(2026, time.July, 21, 0, 0, 0, 0, time.UTC))
	if len(got) != 1 || got[0].MatchStatus != zillow.MatchStatusWatchlist || !containsText(got[0].MatchReasons, "garage") {
		t.Fatalf("garage flex result = %+v", got)
	}
}

func listingIDs(listings []zillow.Listing) []string {
	ids := make([]string, len(listings))
	for index := range listings {
		ids[index] = listings[index].ID
	}
	return ids
}

func containsText(values []string, wanted string) bool {
	for _, value := range values {
		if strings.Contains(strings.ToLower(value), strings.ToLower(wanted)) {
			return true
		}
	}
	return false
}

func TestMergePropertyDetailsPrefersCurrentPropertyFacts(t *testing.T) {
	t.Parallel()

	oldPrice := int64(5000)
	newPrice := int64(4800)
	oldBeds := 3.0
	newBeds := 2.0
	oldBaths := 2.0
	newBaths := 2.5
	oldArea := int64(1200)
	newArea := int64(1300)
	days := int64(1)
	listing := zillow.Listing{
		ID:           "1",
		Price:        &oldPrice,
		Bedrooms:     &oldBeds,
		Bathrooms:    &oldBaths,
		LivingArea:   &oldArea,
		Status:       "FOR_RENT",
		Availability: "2026-08-01",
	}
	property := zillow.Property{
		Price:        &newPrice,
		Bedrooms:     &newBeds,
		Bathrooms:    &newBaths,
		LivingArea:   &newArea,
		DaysOnZillow: &days,
		Status:       "OFF_MARKET",
		Availability: "2026-09-01",
	}
	mergePropertyDetails(&listing, &property)
	if listing.Price == nil || *listing.Price != newPrice || listing.Bedrooms == nil || *listing.Bedrooms != newBeds || listing.Bathrooms == nil || *listing.Bathrooms != newBaths || listing.LivingArea == nil || *listing.LivingArea != newArea {
		t.Fatalf("layout facts = %+v", listing)
	}
	if listing.Status != "OFF_MARKET" || listing.Availability != "2026-09-01" || listing.DaysOnZillow == nil || *listing.DaysOnZillow != 1 {
		t.Fatalf("current facts = %+v", listing)
	}
}

func TestFilterDetailedListingsExcludesSharedAndStudentHousing(t *testing.T) {
	t.Parallel()

	listings := []zillow.Listing{
		{ID: "normal", Status: "FOR_RENT"},
		{ID: "shared", Status: "FOR_RENT", SharedHousing: true},
		{ID: "student", Status: "FOR_RENT", StudentHousing: true},
		{ID: "income", Status: "FOR_RENT", IncomeRestricted: true},
	}
	options := detailFilterOptions{
		Workers:                 1,
		MaxDaysOnZillow:         -1,
		Laundry:                 filterAny,
		Parking:                 filterAny,
		Pets:                    filterAny,
		UnknownLaundry:          unknownLaundryExclude,
		ExcludeSharedHousing:    true,
		ExcludeStudentHousing:   true,
		ExcludeIncomeRestricted: true,
	}
	got := filterDetailedListings(listings, options, time.Date(2026, time.July, 21, 0, 0, 0, 0, time.UTC))
	if ids := listingIDs(got); !reflect.DeepEqual(ids, []string{"normal"}) {
		t.Fatalf("filtered IDs = %v", ids)
	}
}

func TestListingDetailEnricherBackfillsExpandedUnitRecency(t *testing.T) {
	t.Parallel()

	buildingURL := "https://www.zillow.com/apartments/example/ABC/"
	unitURL := "https://www.zillow.com/homedetails/9200_zpid/"
	buildingDays := int64(2)
	days := int64(20)
	fetcher := &fakeRentalPageMapFetcher{pages: map[string]*zillow.RentalPage{
		buildingURL: {
			Kind: zillow.RentalPageCommunity,
			Properties: []zillow.Property{{
				ID: "9200", URL: unitURL, HomeType: "APARTMENT", Status: "FOR_RENT",
			}},
		},
		unitURL: {
			Kind: zillow.RentalPageProperty,
			Properties: []zillow.Property{{
				ID: "9200", URL: unitURL, DaysOnZillow: &days,
				ListedDate: "2026-07-01", UpdatedDate: "2026-07-01",
			}},
		},
	}}
	enricher := newListingDetailEnricher(fetcher, 1, 0, nil)
	expanded := enricher.Enrich(context.Background(), []zillow.Listing{{
		ID: "building", URL: buildingURL, IsBuilding: true,
		DaysOnZillow: &buildingDays, ListedDate: "2026-07-19", UpdatedDate: "2026-07-20",
	}})
	if len(expanded) != 1 || expanded[0].DaysOnZillow != nil || expanded[0].ListedDate != "" || expanded[0].UpdatedDate != "" {
		t.Fatalf("expanded unit inherited building recency = %+v", expanded)
	}
	backfilled := enricher.EnrichRecency(context.Background(), expanded)
	if len(backfilled) != 1 || backfilled[0].DaysOnZillow == nil || *backfilled[0].DaysOnZillow != 20 || backfilled[0].ListedDate != "2026-07-01" || backfilled[0].UpdatedDate != "2026-07-01" {
		t.Fatalf("backfilled listings = %+v", backfilled)
	}
	if fetcher.calls[buildingURL] != 1 || fetcher.calls[unitURL] != 1 {
		t.Fatalf("fetch calls = %+v", fetcher.calls)
	}
}

type fakeRentalPageMapFetcher struct {
	mu    sync.Mutex
	pages map[string]*zillow.RentalPage
	calls map[string]int
}

func (fetcher *fakeRentalPageMapFetcher) FetchRentalPage(_ context.Context, url string) (*zillow.RentalPage, error) {
	fetcher.mu.Lock()
	defer fetcher.mu.Unlock()
	if fetcher.calls == nil {
		fetcher.calls = make(map[string]int)
	}
	fetcher.calls[url]++
	page, ok := fetcher.pages[url]
	if !ok {
		return nil, errors.New("unexpected URL")
	}
	return page, nil
}
