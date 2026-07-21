package cli

import (
	"testing"
	"time"

	"gozillo/internal/zillow"
)

func TestBuildLocationDiscoveryOptionsCreatesRouteMatrix(t *testing.T) {
	t.Parallel()

	options, err := buildLocationDiscoveryOptions(locationDiscoveryConfig{
		MaxPages:      3,
		PageDelay:     5 * time.Second,
		BedRanges:     []string{"2", "3+"},
		ServerSorts:   []string{"days", "mostrecentchange"},
		HomeTypes:     []string{"apartment", "condo", "townhouse", "single-family"},
		ForRent:       true,
		InUnitLaundry: true,
		BaseFilters: zillow.SearchFilters{
			MaxPrice: 5500,
			MinBaths: 2,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if options.MaxPages != 3 || options.PageDelay != 5*time.Second {
		t.Fatalf("options = %+v", options)
	}
	if len(options.Routes) != 4 {
		t.Fatalf("routes = %d, want 4", len(options.Routes))
	}
	want := []struct {
		name    string
		minBeds float64
		maxBeds float64
		sort    string
	}{
		{name: "beds-2-sort-days", minBeds: 2, maxBeds: 2, sort: "days"},
		{name: "beds-2-sort-mostrecentchange", minBeds: 2, maxBeds: 2, sort: "mostrecentchange"},
		{name: "beds-3plus-sort-days", minBeds: 3, sort: "days"},
		{name: "beds-3plus-sort-mostrecentchange", minBeds: 3, sort: "mostrecentchange"},
	}
	for index, expected := range want {
		route := options.Routes[index]
		if route.Name != expected.name || route.Filters.MinBeds != expected.minBeds || route.Filters.MaxBeds != expected.maxBeds || route.Filters.Sort != expected.sort {
			t.Errorf("route %d = %+v, want %+v", index, route, expected)
		}
		if route.Filters.MaxPrice != 5500 || route.Filters.MinBaths != 2 || !route.Filters.InUnitLaundry || !route.Filters.EntirePlaceOnly {
			t.Errorf("route %d did not inherit shared filters: %+v", index, route.Filters)
		}
		if len(route.Filters.HomeTypes) != 4 {
			t.Errorf("route %d home types = %v", index, route.Filters.HomeTypes)
		}
	}
}

func TestBuildLocationDiscoveryOptionsRejectsInvalidRouteInput(t *testing.T) {
	t.Parallel()

	tests := []locationDiscoveryConfig{
		{MaxPages: 3, BedRanges: []string{"2-"}},
		{MaxPages: 3, BedRanges: []string{"4-2"}},
		{MaxPages: 3, ServerSorts: []string{" days"}},
		{MaxPages: 3, HomeTypes: []string{"castle"}},
	}
	for _, config := range tests {
		if _, err := buildLocationDiscoveryOptions(config); err == nil {
			t.Errorf("buildLocationDiscoveryOptions(%+v) error = nil", config)
		}
	}
}

func TestBasicFiltersDeferBuildingLayoutUntilUnitExpansion(t *testing.T) {
	t.Parallel()

	maxBedrooms := 3.0
	building := zillow.Listing{ID: "building", IsBuilding: true, Bedrooms: &maxBedrooms, HomeType: "APARTMENT"}
	filters := zillow.SearchFilters{MaxPrice: 5500, MinBeds: 2, MaxBeds: 2, MinBaths: 2, HomeTypes: []string{zillow.HomeTypeApartment}}
	if got := filterSnapshotListings([]zillow.Listing{building}, filters); len(got) != 1 {
		t.Fatalf("building prefilter removed before unit expansion: %+v", got)
	}

	price := int64(5000)
	beds := 3.0
	baths := 2.0
	unit := zillow.Listing{ID: "unit", Price: &price, Bedrooms: &beds, Bathrooms: &baths, HomeType: "APARTMENT"}
	if got := filterSnapshotListings([]zillow.Listing{unit}, filters); len(got) != 0 {
		t.Fatalf("expanded nonmatching unit survived filters: %+v", got)
	}
}

func TestBuildLocationDiscoveryOptionsSupportsPerSortPageLimits(t *testing.T) {
	t.Parallel()

	options, err := buildLocationDiscoveryOptions(locationDiscoveryConfig{
		MaxPages:    3,
		BedRanges:   []string{"2", "3+"},
		ServerSorts: []string{"days:3", "mostrecentchange:1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(options.Routes) != 4 {
		t.Fatalf("routes = %d", len(options.Routes))
	}
	want := []int{3, 1, 3, 1}
	for index := range want {
		if options.Routes[index].MaxPages != want[index] {
			t.Errorf("route %d max pages = %d, want %d", index, options.Routes[index].MaxPages, want[index])
		}
	}
}

func TestApplyLocationPageOverrideClampsRouteDepth(t *testing.T) {
	t.Parallel()

	overrides, err := parseLocationPageOverrides([]string{"Priority CA=3", "Secondary CA=2"}, 3)
	if err != nil {
		t.Fatal(err)
	}
	base := zillow.DiscoveryOptions{
		MaxPages: 3,
		Routes: []zillow.SearchRoute{
			{Name: "newest", MaxPages: 3},
			{Name: "updated", MaxPages: 1},
		},
	}
	got := applyLocationPageOverride(base, "Secondary CA", overrides)
	if got.MaxPages != 2 || got.Routes[0].MaxPages != 2 || got.Routes[1].MaxPages != 1 {
		t.Fatalf("overridden options = %+v", got)
	}
	if base.MaxPages != 3 || base.Routes[0].MaxPages != 3 {
		t.Fatalf("base options mutated = %+v", base)
	}
}

func TestBuildLocationDiscoveryOptionsAddsSupplementalLaundryAndKeywordRoutes(t *testing.T) {
	t.Parallel()

	options, err := buildLocationDiscoveryOptions(locationDiscoveryConfig{
		MaxPages:              3,
		BedRanges:             []string{"2", "3+"},
		ServerSorts:           []string{"days:2"},
		HomeTypes:             []string{"apartment", "condo", "townhouse", "single-family"},
		ForRent:               true,
		InUnitLaundry:         true,
		SupplementalNoLaundry: true,
		SupplementalPages:     1,
		KeywordRoutes:         []string{"den", "private garage"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(options.Routes) != 6 {
		t.Fatalf("routes = %d, want 6", len(options.Routes))
	}
	for _, index := range []int{2, 3, 4, 5} {
		if options.Routes[index].Filters.InUnitLaundry {
			t.Errorf("supplemental route %d unexpectedly requires indexed laundry", index)
		}
		if options.Routes[index].MaxPages != 1 {
			t.Errorf("supplemental route %d max pages = %d", index, options.Routes[index].MaxPages)
		}
	}
	if options.Routes[4].Filters.Keywords != "den" || options.Routes[5].Filters.Keywords != "private garage" {
		t.Fatalf("keyword routes = %+v", options.Routes[4:])
	}
	for _, index := range []int{4, 5} {
		if options.Routes[index].Filters.MinBeds != 2 || options.Routes[index].Filters.MaxBeds != 2 {
			t.Errorf("keyword route %d beds = %+v", index, options.Routes[index].Filters)
		}
	}
}
