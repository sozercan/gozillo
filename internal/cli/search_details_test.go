package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
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
		"--max-total-cost", "--unknown-availability", "--verify-rental-status", "--previous-results",
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
	got := enricher.Enrich(context.Background(), []zillow.Listing{{ID: "building", URL: url}})
	if len(got) != 2 {
		t.Fatalf("expanded listings = %d, want 2", len(got))
	}
	if got[0].ID != "unit-a" || got[1].ID != "unit-b" || got[0].Price == nil || *got[0].Price != 4400 || got[1].Price == nil || *got[1].Price != 5100 {
		t.Fatalf("expanded listings = %+v", got)
	}
	if fetcher.calls[url] != 1 {
		t.Fatalf("fetch calls = %d, want 1", fetcher.calls[url])
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
	expanded := enricher.Enrich(context.Background(), []zillow.Listing{{ID: "building", URL: buildingURL}})
	if len(expanded) != 1 || expanded[0].DaysOnZillow != nil {
		t.Fatalf("expanded listings = %+v", expanded)
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
