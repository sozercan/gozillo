package cli

import (
	"reflect"
	"testing"

	"gozillo/internal/zillow"
)

func TestNormalizeSortBy(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"", "recommended", "price-asc", "price-desc", "newest", "beds-desc", "sqft-desc"} {
		if _, err := normalizeSortBy(value); err != nil {
			t.Fatalf("normalizeSortBy(%q) error = %v", value, err)
		}
	}
	if _, err := normalizeSortBy("unknown"); err == nil {
		t.Fatal("normalizeSortBy() accepted unknown value")
	}
}

func TestSortListings(t *testing.T) {
	t.Parallel()
	int64Pointer := func(value int64) *int64 { return &value }
	floatPointer := func(value float64) *float64 { return &value }
	base := []zillow.Listing{
		{ID: "unknown"},
		{ID: "high", Price: int64Pointer(5000), Bedrooms: floatPointer(4), LivingArea: int64Pointer(1800), DaysOnZillow: int64Pointer(5)},
		{ID: "low", Price: int64Pointer(3000), Bedrooms: floatPointer(2), LivingArea: int64Pointer(900), DaysOnZillow: int64Pointer(1)},
	}
	tests := []struct {
		mode string
		want []string
	}{
		{mode: sortPriceAsc, want: []string{"low", "high", "unknown"}},
		{mode: sortPriceDesc, want: []string{"high", "low", "unknown"}},
		{mode: sortNewest, want: []string{"low", "high", "unknown"}},
		{mode: sortBedsDesc, want: []string{"high", "low", "unknown"}},
		{mode: sortSqftDesc, want: []string{"high", "low", "unknown"}},
	}
	for _, test := range tests {
		listings := append([]zillow.Listing(nil), base...)
		sortListings(listings, test.mode)
		got := []string{listings[0].ID, listings[1].ID, listings[2].ID}
		if !reflect.DeepEqual(got, test.want) {
			t.Fatalf("sortListings(%s) = %#v, want %#v", test.mode, got, test.want)
		}
	}
}
