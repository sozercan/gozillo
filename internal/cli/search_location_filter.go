package cli

import (
	"errors"
	"fmt"
	"strings"
	"unicode"

	"gozillo/internal/zillow"
)

type locationBoundaryOptions struct {
	Strict        bool
	AllowedCities map[string]struct{}
	CityAliases   map[string][]string
}

func parseAllowedCities(values []string) (map[string]struct{}, error) {
	result := make(map[string]struct{})
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			part = normalizeCityName(part)
			if part == "" {
				return nil, errors.New("allowed city must not be empty")
			}
			result[part] = struct{}{}
		}
	}
	return result, nil
}

func parseLocationCityAliases(values []string) (map[string][]string, error) {
	result := make(map[string][]string)
	for _, value := range values {
		separator := strings.LastIndex(value, "=")
		if separator <= 0 || separator == len(value)-1 {
			return nil, fmt.Errorf("invalid city alias %q (want QUERY=CITY)", value)
		}
		query := strings.ToLower(strings.TrimSpace(value[:separator]))
		city := normalizeCityName(value[separator+1:])
		if query == "" || city == "" {
			return nil, fmt.Errorf("invalid city alias %q", value)
		}
		duplicate := false
		for _, existing := range result[query] {
			if existing == city {
				duplicate = true
				break
			}
		}
		if !duplicate {
			result[query] = append(result[query], city)
		}
	}
	return result, nil
}

func filterListingsByLocationBoundary(listings []zillow.Listing, query string, options locationBoundaryOptions, allowUnknown bool) []zillow.Listing {
	if !options.Strict && len(options.AllowedCities) == 0 {
		return listings
	}
	postalCode, isZIP := locationPostalCode(query)
	queryCity, queryState := locationQueryCity(query)
	expectedCities := make(map[string]struct{})
	if queryCity != "" {
		expectedCities[queryCity] = struct{}{}
	}
	for _, alias := range options.CityAliases[strings.ToLower(strings.TrimSpace(query))] {
		expectedCities[alias] = struct{}{}
	}

	filtered := make([]zillow.Listing, 0, len(listings))
	for _, listing := range listings {
		city := normalizeCityName(listing.Address.City)
		if len(options.AllowedCities) > 0 && !(allowUnknown && city == "") {
			if _, allowed := options.AllowedCities[city]; !allowed {
				continue
			}
		}
		actualState := strings.TrimSpace(listing.Address.State)
		if queryState != "" && !(allowUnknown && actualState == "") && !strings.EqualFold(actualState, queryState) {
			continue
		}
		if options.Strict {
			switch {
			case isZIP:
				actual := strings.TrimSpace(listing.Address.PostalCode)
				if !(allowUnknown && actual == "") && actual != postalCode {
					continue
				}
			case len(expectedCities) > 0:
				if !(allowUnknown && city == "") {
					if _, matches := expectedCities[city]; !matches {
						continue
					}
				}
			}
		}
		filtered = append(filtered, listing)
	}
	return filtered
}

func locationPostalCode(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if len(value) != 5 {
		return "", false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return "", false
		}
	}
	return value, true
}

func locationQueryCity(value string) (string, string) {
	fields := strings.Fields(strings.TrimSpace(value))
	if len(fields) < 2 {
		return "", ""
	}
	state := strings.ToUpper(fields[len(fields)-1])
	if len(state) != 2 {
		return "", ""
	}
	for _, character := range state {
		if character < 'A' || character > 'Z' {
			return "", ""
		}
	}
	return normalizeCityName(strings.Join(fields[:len(fields)-1], " ")), state
}

func normalizeCityName(value string) string {
	value = strings.TrimSpace(value)
	var normalized strings.Builder
	spacePending := false
	for _, character := range value {
		switch {
		case unicode.IsLetter(character) || unicode.IsNumber(character):
			if spacePending && normalized.Len() > 0 {
				normalized.WriteByte(' ')
			}
			spacePending = false
			normalized.WriteRune(unicode.ToLower(character))
		default:
			spacePending = true
		}
	}
	return normalized.String()
}
