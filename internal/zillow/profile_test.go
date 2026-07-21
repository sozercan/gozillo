package zillow

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
)

func TestLoadSearchProfileAndApplyFiltersWithoutMutatingOriginal(t *testing.T) {
	t.Parallel()

	profile, err := LoadSearchProfile(strings.NewReader(`{
		"version": 1,
		"endpoint": "/async-create-search-page-state",
		"method": "PUT",
		"referer": "https://www.zillow.com/homes/Seattle,-WA_rb/",
		"searchQueryState": {
			"pagination": {"currentPage": 1},
			"filterState": {"price": {"min": 100000}, "sortSelection": {"value": "globalrelevanceex"}}
		},
		"wants": {"cat1": ["listResults", "mapResults"], "cat2": ["total"]}
	}`))
	if err != nil {
		t.Fatalf("LoadSearchProfile() error = %v", err)
	}

	filtered, err := profile.WithFilters(SearchFilters{
		Page:     3,
		MinPrice: 250000,
		MaxPrice: 900000,
		MinBeds:  2,
		MinBaths: 1.5,
		Sort:     "priced",
	})
	if err != nil {
		t.Fatalf("WithFilters() error = %v", err)
	}

	assertPathNumber(t, filtered.SearchQueryState, 3, "pagination", "currentPage")
	assertPathNumber(t, filtered.SearchQueryState, 250000, "filterState", "price", "min")
	assertPathNumber(t, filtered.SearchQueryState, 900000, "filterState", "price", "max")
	assertPathNumber(t, filtered.SearchQueryState, 2, "filterState", "beds", "min")
	assertPathNumber(t, filtered.SearchQueryState, 1.5, "filterState", "baths", "min")
	assertPathString(t, filtered.SearchQueryState, "priced", "filterState", "sortSelection", "value")

	assertPathNumber(t, profile.SearchQueryState, 1, "pagination", "currentPage")
	assertPathNumber(t, profile.SearchQueryState, 100000, "filterState", "price", "min")
	assertPathString(t, profile.SearchQueryState, "globalrelevanceex", "filterState", "sortSelection", "value")
	if _, ok := lookupPath(profile.SearchQueryState, "filterState", "beds"); ok {
		t.Fatal("WithFilters() mutated the original profile")
	}
}

func TestSearchProfileValidateRequiresSupportedVersion(t *testing.T) {
	t.Parallel()

	for _, version := range []int{-1, 0, 2} {
		version := version
		t.Run(fmt.Sprintf("version_%d", version), func(t *testing.T) {
			t.Parallel()
			profile := validTestProfile()
			profile.Version = version
			if err := profile.Validate(); err == nil {
				t.Fatalf("Validate() error = nil for version %d, want unsupported version error", version)
			}
		})
	}
}

func TestLoadSearchProfileRejectsUnknownAndUnsafeFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		json string
		is   error
	}{
		{
			name: "unknown field",
			json: `{"version":1,"endpoint":"/async-create-search-page-state","method":"PUT","referer":"https://www.zillow.com/","searchQueryState":{},"wants":{"cat1":["listResults"]},"cookie":"secret"}`,
		},
		{
			name: "wrong endpoint",
			json: `{"version":1,"endpoint":"/graphql","method":"PUT","referer":"https://www.zillow.com/","searchQueryState":{},"wants":{"cat1":["listResults"]}}`,
		},
		{
			name: "wrong method",
			json: `{"version":1,"endpoint":"/async-create-search-page-state","method":"POST","referer":"https://www.zillow.com/","searchQueryState":{},"wants":{"cat1":["listResults"]}}`,
		},
		{
			name: "lookalike host",
			json: `{"version":1,"endpoint":"https://www.zillow.com.evil.example/async-create-search-page-state","method":"PUT","referer":"https://www.zillow.com/","searchQueryState":{},"wants":{"cat1":["listResults"]}}`,
			is:   ErrHostNotAllowed,
		},
		{
			name: "subdomain not explicitly allowed",
			json: `{"version":1,"endpoint":"https://api.zillow.com/async-create-search-page-state","method":"PUT","referer":"https://www.zillow.com/","searchQueryState":{},"wants":{"cat1":["listResults"]}}`,
			is:   ErrHostNotAllowed,
		},
		{
			name: "wants missing cat1",
			json: `{"version":1,"endpoint":"/async-create-search-page-state","method":"PUT","referer":"https://www.zillow.com/","searchQueryState":{},"wants":{"cat2":["mapResults"]}}`,
		},
		{
			name: "wants cat1 is not an array",
			json: `{"version":1,"endpoint":"/async-create-search-page-state","method":"PUT","referer":"https://www.zillow.com/","searchQueryState":{},"wants":{"cat1":"listResults"}}`,
		},
		{
			name: "wants cat1 has no listResults",
			json: `{"version":1,"endpoint":"/async-create-search-page-state","method":"PUT","referer":"https://www.zillow.com/","searchQueryState":{},"wants":{"cat1":["mapResults"]}}`,
		},
		{
			name: "wants cat1 contains non-string",
			json: `{"version":1,"endpoint":"/async-create-search-page-state","method":"PUT","referer":"https://www.zillow.com/","searchQueryState":{},"wants":{"cat1":["listResults",1]}}`,
		},
		{
			name: "other wants group is not an array",
			json: `{"version":1,"endpoint":"/async-create-search-page-state","method":"PUT","referer":"https://www.zillow.com/","searchQueryState":{},"wants":{"cat1":["listResults"],"cat2":"mapResults"}}`,
		},
		{
			name: "other wants group contains non-string",
			json: `{"version":1,"endpoint":"/async-create-search-page-state","method":"PUT","referer":"https://www.zillow.com/","searchQueryState":{},"wants":{"cat1":["listResults"],"cat2":[1]}}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := LoadSearchProfile(strings.NewReader(test.json))
			if err == nil {
				t.Fatal("LoadSearchProfile() error = nil, want validation error")
			}
			if test.is != nil && !errors.Is(err, test.is) {
				t.Fatalf("LoadSearchProfile() error = %v, want errors.Is(%v)", err, test.is)
			}
		})
	}
}

func TestApplyFiltersValidatesMergedCapturedPriceBounds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		price   map[string]any
		filters SearchFilters
	}{
		{name: "captured minimum exceeds new maximum", price: map[string]any{"min": json.Number("500000")}, filters: SearchFilters{MaxPrice: 400000}},
		{name: "new minimum exceeds captured maximum", price: map[string]any{"max": json.Number("400000")}, filters: SearchFilters{MinPrice: 500000}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			profile := validTestProfile()
			profile.SearchQueryState["filterState"] = map[string]any{"price": test.price}
			if _, err := profile.WithFilters(test.filters); err == nil {
				t.Fatal("WithFilters() error = nil, want contradictory merged price error")
			}
		})
	}
}

func TestApplyFiltersValidation(t *testing.T) {
	t.Parallel()

	profile := validTestProfile()
	tests := []SearchFilters{
		{Page: -1},
		{MinPrice: -1},
		{MinPrice: 500, MaxPrice: 100},
		{MinBeds: -1},
		{MinBaths: -1},
		{MinBeds: math.NaN()},
		{MinBaths: math.Inf(1)},
		{Sort: " priced "},
	}
	for _, filters := range tests {
		clone := profile.Clone()
		if err := clone.ApplyFilters(filters); err == nil {
			t.Fatalf("ApplyFilters(%+v) error = nil, want validation error", filters)
		}
	}
}

func assertPathNumber(t *testing.T, root map[string]any, want float64, path ...string) {
	t.Helper()
	value, ok := lookupPath(root, path...)
	if !ok {
		t.Fatalf("path %s is missing", strings.Join(path, "."))
	}
	got, ok := floatFromAny(value)
	if !ok || got != want {
		t.Fatalf("path %s = %#v, want %v", strings.Join(path, "."), value, want)
	}
}

func assertPathString(t *testing.T, root map[string]any, want string, path ...string) {
	t.Helper()
	value, ok := lookupPath(root, path...)
	if !ok {
		t.Fatalf("path %s is missing", strings.Join(path, "."))
	}
	got, ok := value.(string)
	if !ok || got != want {
		t.Fatalf("path %s = %#v, want %q", strings.Join(path, "."), value, want)
	}
}

func validTestProfile() *SearchProfile {
	return &SearchProfile{
		Version:  SearchProfileVersion,
		Endpoint: searchEndpointPath,
		Method:   "PUT",
		Referer:  "https://www.zillow.com/homes/Seattle,-WA_rb/",
		SearchQueryState: map[string]any{
			"pagination":  map[string]any{"currentPage": 1},
			"filterState": map[string]any{},
		},
		Wants: map[string]any{"cat1": []any{"listResults"}},
	}
}
