package cli

import (
	"fmt"
	"sort"
	"strings"

	"gozillo/internal/zillow"
)

const (
	sortRecommended = "recommended"
	sortPriceAsc    = "price-asc"
	sortPriceDesc   = "price-desc"
	sortNewest      = "newest"
	sortBedsDesc    = "beds-desc"
	sortSqftDesc    = "sqft-desc"
)

func normalizeSortBy(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return sortRecommended, nil
	}
	switch value {
	case sortRecommended, sortPriceAsc, sortPriceDesc, sortNewest, sortBedsDesc, sortSqftDesc:
		return value, nil
	default:
		return "", fmt.Errorf("unknown sort-by %q (want recommended, price-asc, price-desc, newest, beds-desc, or sqft-desc)", value)
	}
}

func sortListings(listings []zillow.Listing, mode string) {
	if mode == sortRecommended || len(listings) < 2 {
		return
	}
	sort.SliceStable(listings, func(left, right int) bool {
		a, b := listings[left], listings[right]
		switch mode {
		case sortPriceAsc:
			return lessOptionalInt64(a.Price, b.Price, false)
		case sortPriceDesc:
			return lessOptionalInt64(a.Price, b.Price, true)
		case sortNewest:
			return lessOptionalInt64(a.DaysOnZillow, b.DaysOnZillow, false)
		case sortBedsDesc:
			return lessOptionalFloat64(a.Bedrooms, b.Bedrooms, true)
		case sortSqftDesc:
			return lessOptionalInt64(a.LivingArea, b.LivingArea, true)
		default:
			return false
		}
	})
}

func lessOptionalInt64(left, right *int64, descending bool) bool {
	if left == nil {
		return false
	}
	if right == nil {
		return true
	}
	if *left == *right {
		return false
	}
	if descending {
		return *left > *right
	}
	return *left < *right
}

func lessOptionalFloat64(left, right *float64, descending bool) bool {
	if left == nil {
		return false
	}
	if right == nil {
		return true
	}
	if *left == *right {
		return false
	}
	if descending {
		return *left > *right
	}
	return *left < *right
}
