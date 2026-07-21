package zillow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// SearchOptions controls a search call.
type SearchOptions struct {
	Filters    SearchFilters
	IncludeRaw bool
}

type searchPayload struct {
	SearchQueryState map[string]any `json:"searchQueryState"`
	Wants            map[string]any `json:"wants"`
	RequestID        uint64         `json:"requestId"`
	IsDebugRequest   bool           `json:"isDebugRequest"`
}

// Search applies filters to a clone of profile and returns normalized listings.
func (c *Client) Search(ctx context.Context, profile *SearchProfile, filters SearchFilters) (*SearchResult, error) {
	return c.SearchWithOptions(ctx, profile, SearchOptions{Filters: filters})
}

// SearchWithOptions performs PUT /async-create-search-page-state.
func (c *Client) SearchWithOptions(ctx context.Context, profile *SearchProfile, options SearchOptions) (*SearchResult, error) {
	if c == nil {
		return nil, errors.New("search Zillow: client is nil")
	}
	filtered, err := profile.WithFilters(options.Filters)
	if err != nil {
		return nil, fmt.Errorf("search Zillow: %w", err)
	}

	endpoint, err := resolveSearchEndpoint(filtered.Endpoint)
	if err != nil {
		return nil, fmt.Errorf("search Zillow: resolve endpoint: %w", err)
	}
	requestID := c.requestID()
	payload := searchPayload{
		SearchQueryState: filtered.SearchQueryState,
		Wants:            filtered.Wants,
		RequestID:        requestID,
		IsDebugRequest:   false,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("search Zillow: encode request: %w", err)
	}

	data, _, err := c.execute(ctx, requestSpec{
		operation: "search",
		method:    http.MethodPut,
		url:       endpoint,
		referer:   filtered.Referer,
		body:      body,
		kind:      responseJSON,
	})
	if err != nil {
		return nil, err
	}

	result, err := decodeSearchResponse(data, requestID, filtered.SearchQueryState)
	if err != nil {
		return nil, err
	}
	if options.IncludeRaw {
		result.Raw = append(json.RawMessage(nil), data...)
	}
	return result, nil
}

func decodeSearchResponse(data []byte, requestID uint64, queryState map[string]any) (*SearchResult, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()

	var root any
	if err := decoder.Decode(&root); err != nil {
		return nil, &SchemaDriftError{Operation: "search", Path: "response", Detail: "invalid JSON: " + err.Error()}
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return nil, &SchemaDriftError{Operation: "search", Path: "response", Detail: "multiple JSON values"}
	}

	object, ok := root.(map[string]any)
	if !ok {
		return nil, &SchemaDriftError{Operation: "search", Path: "response", Detail: "top-level value must be an object"}
	}

	items, path, ok := findListResults(object)
	if !ok {
		if path != "" {
			return nil, &SchemaDriftError{Operation: "search", Path: path, Detail: "listing results must be an array"}
		}
		return nil, &SchemaDriftError{Operation: "search", Path: "listResults", Detail: "required listing array is missing"}
	}

	listings := make([]Listing, 0, len(items))
	for index, item := range items {
		listingObject, ok := item.(map[string]any)
		if !ok {
			return nil, &SchemaDriftError{
				Operation: "search",
				Path:      fmt.Sprintf("%s[%d]", path, index),
				Detail:    "listing must be an object",
			}
		}
		listing, err := normalizeListing(listingObject)
		if err != nil {
			return nil, &SchemaDriftError{
				Operation: "search",
				Path:      fmt.Sprintf("%s[%d]", path, index),
				Detail:    err.Error(),
			}
		}
		listings = append(listings, listing)
	}

	metadata := SearchMetadata{
		RequestID:   requestID,
		CurrentPage: searchPage(queryState),
		Returned:    len(listings),
	}
	if value, ok := firstPathValue(object,
		[]string{"cat1", "searchList", "totalResultCount"},
		[]string{"cat1", "searchResults", "totalResultCount"},
		[]string{"searchPageState", "cat1", "searchList", "totalResultCount"},
		[]string{"searchPageState", "cat1", "searchResults", "totalResultCount"},
	); ok {
		metadata.TotalResults, _ = intFromAny(value)
	} else if value, ok := findKeyRecursive(object, "totalResultCount"); ok {
		metadata.TotalResults, _ = intFromAny(value)
	}
	if value, ok := firstPathValue(object,
		[]string{"cat1", "searchResults", "resultsHash"},
		[]string{"searchPageState", "cat1", "searchResults", "resultsHash"},
	); ok {
		metadata.ResultsHash, _ = stringFromAny(value)
	}
	if value, ok := firstPathValue(object,
		[]string{"cat1", "searchResults", "relaxedResults"},
		[]string{"searchPageState", "cat1", "searchResults", "relaxedResults"},
	); ok {
		metadata.RelaxedResults, _ = boolFromAny(value)
	}

	return &SearchResult{Listings: listings, Metadata: metadata}, nil
}

func findListResults(root map[string]any) ([]any, string, bool) {
	paths := [][]string{
		{"cat1", "searchResults", "listResults"},
		{"searchPageState", "cat1", "searchResults", "listResults"},
		{"data", "cat1", "searchResults", "listResults"},
	}
	for _, path := range paths {
		if value, ok := lookupPath(root, path...); ok {
			items, ok := value.([]any)
			if !ok {
				return nil, strings.Join(path, "."), false
			}
			return items, strings.Join(path, "."), true
		}
	}

	value, path, ok := findKeyRecursiveWithPath(root, "listResults", "")
	if !ok {
		return nil, "", false
	}
	items, ok := value.([]any)
	if !ok {
		return nil, path, false
	}
	return items, path, true
}

func normalizeListing(raw map[string]any) (Listing, error) {
	if err := validateListingFields(raw); err != nil {
		return Listing{}, err
	}
	listing := Listing{
		ID:        firstString(raw, "zpid", "id"),
		URL:       normalizeZillowURL(firstString(raw, "detailUrl", "hdpUrl", "url")),
		HomeType:  firstString(raw, "homeType", "propertyType"),
		Status:    firstString(raw, "statusType", "homeStatus", "statusText"),
		ImageURL:  firstString(raw, "imgSrc", "imageUrl"),
		PriceText: stringValue(raw["price"]),
	}
	listing.Address = normalizeAddress(raw)
	listing.Bedrooms = floatPointer(raw, "beds", "bedrooms")
	listing.Bathrooms = floatPointer(raw, "baths", "bathrooms")
	listing.LivingArea = int64Pointer(raw, "area", "livingArea")
	listing.Price = moneyPointer(raw, "unformattedPrice", "price")
	listing.Coordinates = normalizeCoordinates(raw)
	listing.DaysOnZillow = int64Pointer(raw, "daysOnZillow")
	if homeInfo, ok := lookupPath(raw, "hdpData", "homeInfo"); ok {
		if home, ok := homeInfo.(map[string]any); ok && listing.DaysOnZillow == nil {
			listing.DaysOnZillow = int64Pointer(home, "daysOnZillow")
		}
	}
	listing.Availability = normalizedAvailability(raw["availabilityDate"])
	if listing.ID == "" && listing.URL == "" && listing.Address.Full == "" {
		return Listing{}, errors.New("listing has no recognized core identity")
	}
	return listing, nil
}

func validateListingFields(raw map[string]any) error {
	if err := validateOptionalListingFields(raw,
		[]string{"zpid", "id", "addressZipcode", "zipcode", "postalCode"},
		"a string or number",
		func(value any) bool {
			_, ok := stringFromAny(value)
			return ok
		},
	); err != nil {
		return err
	}
	if err := validateOptionalListingFields(raw,
		[]string{
			"detailUrl", "hdpUrl", "url", "homeType", "propertyType",
			"statusType", "homeStatus", "statusText", "imgSrc", "imageUrl",
			"addressStreet", "streetAddress", "addressCity", "city", "addressState", "state", "availabilityDate",
		},
		"a string",
		func(value any) bool {
			_, ok := value.(string)
			return ok
		},
	); err != nil {
		return err
	}
	if err := validateOptionalListingFields(raw,
		[]string{"beds", "bedrooms", "baths", "bathrooms", "latitude", "lat", "longitude", "lng", "lon"},
		"numeric",
		func(value any) bool {
			_, ok := floatFromAny(value)
			return ok
		},
	); err != nil {
		return err
	}
	if err := validateOptionalListingFields(raw,
		[]string{"area", "livingArea", "daysOnZillow"},
		"an integer",
		isIntegralValue,
	); err != nil {
		return err
	}
	if err := validateOptionalListingFields(raw,
		[]string{"unformattedPrice"},
		"a money value",
		isMoneyValue,
	); err != nil {
		return err
	}
	if err := validateOptionalListingFields(raw,
		[]string{"price"},
		"a string or number",
		func(value any) bool {
			if _, ok := value.(string); ok {
				return true
			}
			_, ok := floatFromAny(value)
			return ok
		},
	); err != nil {
		return err
	}

	if address, exists := raw["address"]; exists && address != nil {
		switch value := address.(type) {
		case string:
		case map[string]any:
			if err := validateOptionalListingFields(value,
				[]string{"streetAddress", "addressStreet", "city", "addressCity", "state", "addressState", "full", "formattedAddress"},
				"a string",
				func(value any) bool {
					_, ok := value.(string)
					return ok
				},
			); err != nil {
				return fmt.Errorf("address.%w", err)
			}
			if err := validateOptionalListingFields(value,
				[]string{"zipcode", "postalCode", "addressZipcode"},
				"a string or number",
				func(value any) bool {
					_, ok := stringFromAny(value)
					return ok
				},
			); err != nil {
				return fmt.Errorf("address.%w", err)
			}
		default:
			return errors.New("address must be a string or object")
		}
	}

	if latLong, exists := raw["latLong"]; exists && latLong != nil {
		value, ok := latLong.(map[string]any)
		if !ok {
			return errors.New("latLong must be an object")
		}
		if err := validateOptionalListingFields(value,
			[]string{"latitude", "lat", "longitude", "lng", "lon"},
			"numeric",
			func(value any) bool {
				_, ok := floatFromAny(value)
				return ok
			},
		); err != nil {
			return fmt.Errorf("latLong.%w", err)
		}
	}
	return nil
}

func validateOptionalListingFields(raw map[string]any, keys []string, expected string, valid func(any) bool) error {
	for _, key := range keys {
		value, exists := raw[key]
		if !exists || value == nil {
			continue
		}
		if !valid(value) {
			return fmt.Errorf("%s must be %s", key, expected)
		}
	}
	return nil
}

func isIntegralValue(value any) bool {
	if _, ok := int64FromAny(value); !ok {
		return false
	}
	if number, ok := floatFromAny(value); ok {
		return math.Trunc(number) == number
	}
	return true
}

func isMoneyValue(value any) bool {
	if text, ok := value.(string); ok {
		_, ok := parseMoney(text)
		return ok
	}
	return isIntegralValue(value)
}

func normalizeAddress(raw map[string]any) Address {
	address := Address{
		Street:     firstString(raw, "addressStreet", "streetAddress"),
		City:       firstString(raw, "addressCity", "city"),
		State:      firstString(raw, "addressState", "state"),
		PostalCode: firstString(raw, "addressZipcode", "zipcode", "postalCode"),
	}

	switch value := raw["address"].(type) {
	case string:
		address.Full = strings.TrimSpace(value)
	case map[string]any:
		if address.Street == "" {
			address.Street = firstString(value, "streetAddress", "addressStreet")
		}
		if address.City == "" {
			address.City = firstString(value, "city", "addressCity")
		}
		if address.State == "" {
			address.State = firstString(value, "state", "addressState")
		}
		if address.PostalCode == "" {
			address.PostalCode = firstString(value, "zipcode", "postalCode", "addressZipcode")
		}
		address.Full = firstString(value, "full", "formattedAddress")
	}
	if address.Full == "" {
		address.Full = joinAddress(address)
	}
	return address
}

func joinAddress(address Address) string {
	locality := strings.TrimSpace(strings.Join(nonEmpty(address.City, address.State, address.PostalCode), " "))
	parts := nonEmpty(address.Street, locality)
	return strings.Join(parts, ", ")
}

func nonEmpty(values ...string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result = append(result, value)
		}
	}
	return result
}

func normalizeCoordinates(raw map[string]any) Coordinates {
	latitude := floatPointer(raw, "latitude", "lat")
	longitude := floatPointer(raw, "longitude", "lng", "lon")
	if nested, ok := raw["latLong"].(map[string]any); ok {
		if latitude == nil {
			latitude = floatPointer(nested, "latitude", "lat")
		}
		if longitude == nil {
			longitude = floatPointer(nested, "longitude", "lng", "lon")
		}
	}
	return Coordinates{Latitude: latitude, Longitude: longitude}
}

func searchPage(queryState map[string]any) int {
	value, ok := lookupPath(queryState, "pagination", "currentPage")
	if !ok {
		return 0
	}
	page, _ := intFromAny(value)
	return page
}

func lookupPath(root map[string]any, path ...string) (any, bool) {
	var current any = root
	for _, key := range path {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = object[key]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func firstPathValue(root map[string]any, paths ...[]string) (any, bool) {
	for _, path := range paths {
		if value, ok := lookupPath(root, path...); ok {
			return value, true
		}
	}
	return nil, false
}

func findKeyRecursive(root any, wanted string) (any, bool) {
	value, _, ok := findKeyRecursiveWithPath(root, wanted, "")
	return value, ok
}

func findKeyRecursiveWithPath(root any, wanted, prefix string) (any, string, bool) {
	switch value := root.(type) {
	case map[string]any:
		if found, ok := value[wanted]; ok {
			path := wanted
			if prefix != "" {
				path = prefix + "." + wanted
			}
			return found, path, true
		}
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			path := key
			if prefix != "" {
				path = prefix + "." + key
			}
			if found, foundPath, ok := findKeyRecursiveWithPath(value[key], wanted, path); ok {
				return found, foundPath, true
			}
		}
	case []any:
		for index, item := range value {
			path := fmt.Sprintf("%s[%d]", prefix, index)
			if found, foundPath, ok := findKeyRecursiveWithPath(item, wanted, path); ok {
				return found, foundPath, true
			}
		}
	}
	return nil, "", false
}

func firstString(raw map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := stringFromAny(raw[key]); ok && value != "" {
			return value
		}
	}
	return ""
}

func stringValue(value any) string {
	text, _ := stringFromAny(value)
	return text
}

func stringFromAny(value any) (string, bool) {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed), true
	case json.Number:
		return typed.String(), true
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64), true
	case float32:
		return strconv.FormatFloat(float64(typed), 'f', -1, 64), true
	case int:
		return strconv.Itoa(typed), true
	case int64:
		return strconv.FormatInt(typed, 10), true
	case int32:
		return strconv.FormatInt(int64(typed), 10), true
	case uint64:
		return strconv.FormatUint(typed, 10), true
	case uint:
		return strconv.FormatUint(uint64(typed), 10), true
	default:
		return "", false
	}
}

func floatPointer(raw map[string]any, keys ...string) *float64 {
	for _, key := range keys {
		if value, ok := floatFromAny(raw[key]); ok {
			copy := value
			return &copy
		}
	}
	return nil
}

func int64Pointer(raw map[string]any, keys ...string) *int64 {
	for _, key := range keys {
		if value, ok := int64FromAny(raw[key]); ok {
			copy := value
			return &copy
		}
	}
	return nil
}

func moneyPointer(raw map[string]any, keys ...string) *int64 {
	for _, key := range keys {
		if value, ok := int64FromAny(raw[key]); ok {
			copy := value
			return &copy
		}
		if text, ok := raw[key].(string); ok {
			if value, ok := parseMoney(text); ok {
				copy := value
				return &copy
			}
		}
	}
	return nil
}

func floatFromAny(value any) (float64, bool) {
	switch typed := value.(type) {
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	case float64:
		return typed, !math.IsNaN(typed) && !math.IsInf(typed, 0)
	case float32:
		parsed := float64(typed)
		return parsed, !math.IsNaN(parsed) && !math.IsInf(parsed, 0)
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case int32:
		return float64(typed), true
	case uint64:
		return float64(typed), true
	case uint:
		return float64(typed), true
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(typed), 64)
		return parsed, err == nil && !math.IsNaN(parsed) && !math.IsInf(parsed, 0)
	default:
		return 0, false
	}
}

func int64FromAny(value any) (int64, bool) {
	switch typed := value.(type) {
	case json.Number:
		if parsed, err := typed.Int64(); err == nil {
			return parsed, true
		}
		rational, ok := new(big.Rat).SetString(typed.String())
		if !ok || !rational.IsInt() || !rational.Num().IsInt64() {
			return 0, false
		}
		return rational.Num().Int64(), true
	case int:
		return int64(typed), true
	case int64:
		return typed, true
	case int32:
		return int64(typed), true
	case uint:
		if uint64(typed) > math.MaxInt64 {
			return 0, false
		}
		return int64(typed), true
	case uint64:
		if typed > math.MaxInt64 {
			return 0, false
		}
		return int64(typed), true
	case float64:
		const maxInt64Exclusive = 9223372036854775808.0
		if math.IsNaN(typed) || math.IsInf(typed, 0) || math.Trunc(typed) != typed || typed < -maxInt64Exclusive || typed >= maxInt64Exclusive {
			return 0, false
		}
		return int64(typed), true
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func intFromAny(value any) (int, bool) {
	parsed, ok := int64FromAny(value)
	if !ok || int64(int(parsed)) != parsed {
		return 0, false
	}
	return int(parsed), true
}

func boolFromAny(value any) (bool, bool) {
	switch typed := value.(type) {
	case bool:
		return typed, true
	case string:
		parsed, err := strconv.ParseBool(strings.TrimSpace(typed))
		return parsed, err == nil
	default:
		return false, false
	}
}

func parseMoney(value string) (int64, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}

	start, end, ok := oneNumericToken(value)
	if !ok || hasNegativeMoneySign(value[:start]) {
		return 0, false
	}
	number, ok := normalizeNumericToken(value[start:end])
	if !ok {
		return 0, false
	}

	multiplier := float64(1)
	if end < len(value) && (end+1 == len(value) || !isASCIIAlphaNumeric(value[end+1])) {
		switch value[end] {
		case 'k', 'K':
			multiplier = 1_000
		case 'm', 'M':
			multiplier = 1_000_000
		case 'b', 'B':
			multiplier = 1_000_000_000
		}
	}

	parsed, err := strconv.ParseFloat(number, 64)
	scaled := parsed * multiplier
	if err != nil || parsed < 0 || math.IsInf(scaled, 0) || scaled >= float64(math.MaxInt64) {
		return 0, false
	}
	return int64(math.Round(scaled)), true
}

func hasNegativeMoneySign(prefix string) bool {
	prefix = strings.TrimRightFunc(prefix, unicode.IsSpace)
	for prefix != "" {
		character, size := utf8.DecodeLastRuneInString(prefix)
		switch {
		case character == '-' || character == '−':
			return true
		case unicode.Is(unicode.Sc, character):
			prefix = strings.TrimRightFunc(prefix[:len(prefix)-size], unicode.IsSpace)
		default:
			return false
		}
	}
	return false
}

func oneNumericToken(value string) (int, int, bool) {
	start, end := -1, -1
	for index := 0; index < len(value); {
		if !isASCIIDigit(value[index]) {
			index++
			continue
		}
		if start >= 0 {
			return 0, 0, false
		}
		start = index
		index = scanNumericToken(value, index)
		end = index
	}
	return start, end, start >= 0
}

func scanNumericToken(value string, start int) int {
	index := start
	for index < len(value) && isASCIIDigit(value[index]) {
		index++
	}

	if index-start <= 3 {
		for index < len(value) && value[index] == ',' {
			groupStart := index + 1
			groupEnd := groupStart
			for groupEnd < len(value) && isASCIIDigit(value[groupEnd]) {
				groupEnd++
			}
			if groupEnd-groupStart != 3 {
				break
			}
			index = groupEnd
		}
	}

	if index+1 < len(value) && value[index] == '.' && isASCIIDigit(value[index+1]) {
		index += 2
		for index < len(value) && isASCIIDigit(value[index]) {
			index++
		}
	}
	return index
}

func normalizeNumericToken(value string) (string, bool) {
	parts := strings.Split(value, ".")
	if len(parts) > 2 || parts[0] == "" {
		return "", false
	}
	if len(parts) == 2 && (parts[1] == "" || !allASCIIDigits(parts[1])) {
		return "", false
	}

	integer := parts[0]
	if strings.Contains(integer, ",") {
		groups := strings.Split(integer, ",")
		if len(groups[0]) < 1 || len(groups[0]) > 3 || !allASCIIDigits(groups[0]) {
			return "", false
		}
		for _, group := range groups[1:] {
			if len(group) != 3 || !allASCIIDigits(group) {
				return "", false
			}
		}
	} else if !allASCIIDigits(integer) {
		return "", false
	}

	normalized := strings.ReplaceAll(integer, ",", "")
	if len(parts) == 2 {
		normalized += "." + parts[1]
	}
	return normalized, true
}

func allASCIIDigits(value string) bool {
	if value == "" {
		return false
	}
	for index := range len(value) {
		if !isASCIIDigit(value[index]) {
			return false
		}
	}
	return true
}

func isASCIIDigit(value byte) bool {
	return value >= '0' && value <= '9'
}

func isASCIIAlphaNumeric(value byte) bool {
	return isASCIIDigit(value) || value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z'
}

func normalizeZillowURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	if parsed.IsAbs() {
		return parsed.String()
	}
	base, _ := url.Parse("https://www.zillow.com")
	return base.ResolveReference(parsed).String()
}
