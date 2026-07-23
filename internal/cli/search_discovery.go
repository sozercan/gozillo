package cli

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode"

	"gozillo/internal/zillow"
)

const maxLocationDiscoveryPages = 20

type locationDiscoveryConfig struct {
	MaxPages              int
	PageDelay             time.Duration
	RequestRetries        int
	RetryBackoff          time.Duration
	BedRanges             []string
	ServerSorts           []string
	HomeTypes             []string
	ForRent               bool
	InUnitLaundry         bool
	SupplementalNoLaundry bool
	SupplementalPages     int
	KeywordRoutes         []string
	BaseFilters           zillow.SearchFilters
}

type parsedBedRange struct {
	label string
	min   float64
	max   float64
}

type parsedServerSort struct {
	value    string
	maxPages int
}

func filterListingsByDiscoveryBedRanges(listings []zillow.Listing, routes []zillow.SearchRoute, allowUnknown bool) []zillow.Listing {
	if len(routes) == 0 {
		return listings
	}

	filtered := make([]zillow.Listing, 0, len(listings))
	for _, listing := range listings {
		if listing.IsBuilding || (allowUnknown && listing.Bedrooms == nil) || listingMatchesDiscoveryBedRanges(listing, routes) {
			filtered = append(filtered, listing)
		}
	}
	return filtered
}

func listingMatchesDiscoveryBedRanges(listing zillow.Listing, routes []zillow.SearchRoute) bool {
	for _, route := range routes {
		minimum := route.Filters.MinBeds
		maximum := route.Filters.MaxBeds
		if minimum <= 0 && maximum <= 0 {
			return true
		}
		if listing.Bedrooms == nil {
			continue
		}
		if minimum > 0 && *listing.Bedrooms < minimum {
			continue
		}
		if maximum > 0 && *listing.Bedrooms > maximum {
			continue
		}
		return true
	}
	return false
}

func buildLocationDiscoveryOptions(config locationDiscoveryConfig) (zillow.DiscoveryOptions, error) {
	maxPages := config.MaxPages
	if maxPages == 0 {
		maxPages = 1
	}
	if maxPages < 1 || maxPages > maxLocationDiscoveryPages {
		return zillow.DiscoveryOptions{}, fmt.Errorf("max-pages must be between 1 and %d", maxLocationDiscoveryPages)
	}
	if config.PageDelay < 0 {
		return zillow.DiscoveryOptions{}, errors.New("page-delay must not be negative")
	}
	supplementalPages := config.SupplementalPages
	if supplementalPages == 0 {
		supplementalPages = 1
	}
	if supplementalPages < 1 || supplementalPages > maxPages {
		return zillow.DiscoveryOptions{}, fmt.Errorf("supplemental-pages must be between 1 and max-pages %d", maxPages)
	}
	keywords, err := parseKeywordRoutes(config.KeywordRoutes)
	if err != nil {
		return zillow.DiscoveryOptions{}, fmt.Errorf("keyword-route: %w", err)
	}

	bedRanges, err := parseBedRanges(config.BedRanges, config.BaseFilters.MinBeds, config.BaseFilters.MaxBeds)
	if err != nil {
		return zillow.DiscoveryOptions{}, fmt.Errorf("bed-range: %w", err)
	}
	sorts, err := parseServerSorts(config.ServerSorts, config.BaseFilters.Sort)
	if err != nil {
		return zillow.DiscoveryOptions{}, fmt.Errorf("server-sort: %w", err)
	}
	homeTypes, err := parseHomeTypes(config.HomeTypes)
	if err != nil {
		return zillow.DiscoveryOptions{}, fmt.Errorf("home-type: %w", err)
	}

	routes := make([]zillow.SearchRoute, 0, len(bedRanges)*len(sorts))
	for _, beds := range bedRanges {
		for _, sortSpec := range sorts {
			filters := config.BaseFilters
			filters.Page = 0
			filters.MinBeds = beds.min
			filters.MaxBeds = beds.max
			filters.Sort = sortSpec.value
			filters.HomeTypes = append([]string(nil), homeTypes...)
			filters.InUnitLaundry = config.InUnitLaundry
			filters.EntirePlaceOnly = config.ForRent
			sortLabel := sortSpec.value
			if sortLabel == "" {
				sortLabel = "default"
			}
			if sortSpec.maxPages > maxPages {
				return zillow.DiscoveryOptions{}, fmt.Errorf("server-sort %q page limit %d exceeds max-pages %d", sortSpec.value, sortSpec.maxPages, maxPages)
			}
			routes = append(routes, zillow.SearchRoute{
				Name:     "beds-" + beds.label + "-sort-" + sortLabel,
				Filters:  filters,
				MaxPages: sortSpec.maxPages,
			})
		}
	}
	if config.SupplementalNoLaundry {
		for _, beds := range bedRanges {
			filters := config.BaseFilters
			filters.Page = 0
			filters.MinBeds = beds.min
			filters.MaxBeds = beds.max
			filters.Sort = "days"
			filters.Keywords = ""
			filters.HomeTypes = append([]string(nil), homeTypes...)
			filters.InUnitLaundry = false
			filters.EntirePlaceOnly = config.ForRent
			routes = append(routes, zillow.SearchRoute{
				Name:     "beds-" + beds.label + "-supplemental-unindexed-laundry",
				Filters:  filters,
				MaxPages: supplementalPages,
			})
		}
	}
	for index, keyword := range keywords {
		filters := config.BaseFilters
		filters.Page = 0
		filters.MinBeds = 2
		filters.MaxBeds = 2
		filters.Sort = "days"
		filters.Keywords = keyword
		filters.HomeTypes = append([]string(nil), homeTypes...)
		filters.InUnitLaundry = false
		filters.EntirePlaceOnly = config.ForRent
		routes = append(routes, zillow.SearchRoute{
			Name:     fmt.Sprintf("exact-2-keyword-%d", index+1),
			Filters:  filters,
			MaxPages: supplementalPages,
		})
	}
	if len(routes) > 16 {
		return zillow.DiscoveryOptions{}, errors.New("route matrix exceeds 16 combinations")
	}
	return zillow.DiscoveryOptions{
		Routes: routes, MaxPages: maxPages, PageDelay: config.PageDelay,
		RequestRetries: config.RequestRetries, RetryBackoff: config.RetryBackoff,
	}, nil
}

func parseBedRanges(values []string, fallbackMin, fallbackMax float64) ([]parsedBedRange, error) {
	if len(values) == 0 {
		label := bedRangeLabel(fallbackMin, fallbackMax)
		return []parsedBedRange{{label: label, min: fallbackMin, max: fallbackMax}}, nil
	}
	result := make([]parsedBedRange, 0, len(values))
	seen := make(map[string]struct{})
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				return nil, errors.New("contains an empty comma-separated value")
			}
			parsed, err := parseBedRange(part)
			if err != nil {
				return nil, err
			}
			if _, exists := seen[parsed.label]; exists {
				continue
			}
			seen[parsed.label] = struct{}{}
			result = append(result, parsed)
		}
	}
	return result, nil
}

func parseBedRange(value string) (parsedBedRange, error) {
	if strings.HasSuffix(value, "+") {
		minimum, err := parseNonNegativeFloat(strings.TrimSuffix(value, "+"))
		if err != nil || minimum == 0 {
			return parsedBedRange{}, fmt.Errorf("invalid open-ended range %q", value)
		}
		return parsedBedRange{label: formatBedNumber(minimum) + "plus", min: minimum}, nil
	}
	if strings.Contains(value, "-") {
		parts := strings.Split(value, "-")
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return parsedBedRange{}, fmt.Errorf("invalid closed range %q", value)
		}
		minimum, err := parseNonNegativeFloat(parts[0])
		if err != nil {
			return parsedBedRange{}, fmt.Errorf("invalid closed range %q", value)
		}
		maximum, err := parseNonNegativeFloat(parts[1])
		if err != nil || minimum == 0 || maximum == 0 || minimum > maximum {
			return parsedBedRange{}, fmt.Errorf("invalid closed range %q", value)
		}
		return parsedBedRange{label: formatBedNumber(minimum) + "to" + formatBedNumber(maximum), min: minimum, max: maximum}, nil
	}
	exact, err := parseNonNegativeFloat(value)
	if err != nil || exact == 0 {
		return parsedBedRange{}, fmt.Errorf("invalid exact range %q", value)
	}
	return parsedBedRange{label: formatBedNumber(exact), min: exact, max: exact}, nil
}

func parseNonNegativeFloat(value string) (float64, error) {
	parsed, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) || parsed < 0 {
		return 0, errors.New("must be a non-negative finite number")
	}
	return parsed, nil
}

func formatBedNumber(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}

func bedRangeLabel(minimum, maximum float64) string {
	switch {
	case minimum > 0 && maximum > 0 && minimum == maximum:
		return formatBedNumber(minimum)
	case minimum > 0 && maximum > 0:
		return formatBedNumber(minimum) + "to" + formatBedNumber(maximum)
	case minimum > 0:
		return formatBedNumber(minimum) + "plus"
	case maximum > 0:
		return "upto" + formatBedNumber(maximum)
	default:
		return "any"
	}
}

func parseServerSorts(values []string, fallback string) ([]parsedServerSort, error) {
	if len(values) == 0 {
		if err := validateServerSort(fallback); err != nil {
			return nil, err
		}
		return []parsedServerSort{{value: fallback}}, nil
	}
	result := make([]parsedServerSort, 0, len(values))
	seen := make(map[string]struct{})
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			spec, err := parseServerSort(part)
			if err != nil {
				return nil, err
			}
			key := fmt.Sprintf("%s:%d", spec.value, spec.maxPages)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			result = append(result, spec)
		}
	}
	return result, nil
}

func parseServerSort(value string) (parsedServerSort, error) {
	if value != strings.TrimSpace(value) {
		return parsedServerSort{}, errors.New("must not have surrounding whitespace")
	}
	parts := strings.Split(value, ":")
	if len(parts) > 2 || parts[0] == "" {
		return parsedServerSort{}, fmt.Errorf("invalid value %q", value)
	}
	if err := validateServerSort(parts[0]); err != nil {
		return parsedServerSort{}, err
	}
	spec := parsedServerSort{value: parts[0]}
	if len(parts) == 2 {
		pages, err := strconv.Atoi(parts[1])
		if err != nil || pages < 1 || pages > maxLocationDiscoveryPages {
			return parsedServerSort{}, fmt.Errorf("invalid page limit in %q", value)
		}
		spec.maxPages = pages
	}
	return spec, nil
}

func validateServerSort(value string) error {
	if value != strings.TrimSpace(value) {
		return errors.New("must not have surrounding whitespace")
	}
	if value == "" {
		return nil
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return errors.New("must not contain control characters")
		}
	}
	return nil
}

func parseHomeTypes(values []string) ([]string, error) {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{})
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			part = strings.ToLower(strings.TrimSpace(part))
			if part == "" {
				return nil, errors.New("contains an empty comma-separated value")
			}
			switch part {
			case zillow.HomeTypeApartment, zillow.HomeTypeCondo, zillow.HomeTypeTownhouse, zillow.HomeTypeSingleFamily:
			default:
				return nil, fmt.Errorf("unknown value %q", part)
			}
			if _, exists := seen[part]; exists {
				continue
			}
			seen[part] = struct{}{}
			result = append(result, part)
		}
	}
	return result, nil
}

func parseKeywordRoutes(values []string) ([]string, error) {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{})
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			return nil, errors.New("keyword must not be empty")
		}
		if len(value) > 256 {
			return nil, errors.New("keyword must not exceed 256 bytes")
		}
		for _, character := range value {
			if unicode.IsControl(character) {
				return nil, errors.New("keyword must not contain control characters")
			}
		}
		key := strings.ToLower(value)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	return result, nil
}

func parseLocationPageOverrides(values []string, globalMax int) (map[string]int, error) {
	result := make(map[string]int, len(values))
	for _, value := range values {
		separator := strings.LastIndex(value, "=")
		if separator <= 0 || separator == len(value)-1 {
			return nil, fmt.Errorf("invalid location page override %q (want LOCATION=PAGES)", value)
		}
		location := strings.TrimSpace(value[:separator])
		pages, err := strconv.Atoi(strings.TrimSpace(value[separator+1:]))
		if location == "" || err != nil || pages < 1 || pages > globalMax {
			return nil, fmt.Errorf("invalid location page override %q", value)
		}
		key := strings.ToLower(location)
		if _, exists := result[key]; exists {
			return nil, fmt.Errorf("duplicate location page override %q", location)
		}
		result[key] = pages
	}
	return result, nil
}

func applyLocationPageOverride(options zillow.DiscoveryOptions, location string, overrides map[string]int) zillow.DiscoveryOptions {
	pages, exists := overrides[strings.ToLower(strings.TrimSpace(location))]
	if !exists {
		return options
	}
	clone := options
	clone.MaxPages = pages
	clone.Routes = append([]zillow.SearchRoute(nil), options.Routes...)
	for index := range clone.Routes {
		if clone.Routes[index].MaxPages > pages {
			clone.Routes[index].MaxPages = pages
		}
	}
	return clone
}
