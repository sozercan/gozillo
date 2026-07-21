package zillow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"unicode"
)

// PropertyOptions controls property ingestion.
type PropertyOptions struct {
	IncludeRaw bool
}

// PropertyReaderOptions controls property ingestion from an arbitrary reader.
type PropertyReaderOptions struct {
	IncludeRaw bool
	MaxBytes   int64
}

// FetchProperty downloads and parses a Zillow property page.
func (c *Client) FetchProperty(ctx context.Context, rawURL string) (*Property, error) {
	return c.FetchPropertyWithOptions(ctx, rawURL, PropertyOptions{})
}

// PropertyFromURL is an alias for FetchProperty.
func (c *Client) PropertyFromURL(ctx context.Context, rawURL string) (*Property, error) {
	return c.FetchProperty(ctx, rawURL)
}

// FetchPropertyWithOptions downloads and parses a Zillow property page.
func (c *Client) FetchPropertyWithOptions(ctx context.Context, rawURL string, options PropertyOptions) (*Property, error) {
	if c == nil {
		return nil, errors.New("fetch Zillow property: client is nil")
	}
	target, err := parseAllowedZillowURL(rawURL)
	if err != nil {
		return nil, fmt.Errorf("fetch Zillow property: %w", err)
	}
	target.Fragment = ""

	data, _, err := c.execute(ctx, requestSpec{
		operation: "property",
		method:    http.MethodGet,
		url:       target,
		kind:      responseHTML,
	})
	if err != nil {
		return nil, err
	}

	property, err := parsePropertyDocumentForZPID(data, options.IncludeRaw, zpidFromPath(target.Path))
	if err != nil {
		return nil, err
	}
	if property.URL == "" {
		property.URL = target.String()
	}
	return property, nil
}

// ReadProperty parses a Zillow property page from a reader.
func ReadProperty(reader io.Reader) (*Property, error) {
	return ReadPropertyWithOptions(reader, PropertyReaderOptions{})
}

// ParsePropertyPage is an alias for ReadProperty.
func ParsePropertyPage(reader io.Reader) (*Property, error) {
	return ReadProperty(reader)
}

// PropertyFromReader parses a Zillow property page from a reader.
func (c *Client) PropertyFromReader(reader io.Reader) (*Property, error) {
	return ReadProperty(reader)
}

// ReadPropertyWithOptions parses a bounded Zillow property page from a reader.
func ReadPropertyWithOptions(reader io.Reader, options PropertyReaderOptions) (*Property, error) {
	if reader == nil {
		return nil, errors.New("read Zillow property: reader is nil")
	}
	limit := options.MaxBytes
	if limit == 0 {
		limit = DefaultMaxResponseBytes
	}
	if limit < 0 {
		return nil, errors.New("read Zillow property: maximum size must be positive")
	}

	data, err := readBounded(reader, limit)
	if err != nil {
		if errors.Is(err, ErrResponseTooLarge) {
			return nil, &ResponseTooLargeError{Limit: limit}
		}
		return nil, fmt.Errorf("read Zillow property: %w", err)
	}
	if reason := detectChallenge(responseHTML, http.StatusOK, "text/html", data); reason != "" {
		return nil, &ChallengeError{StatusCode: http.StatusOK, Reason: reason}
	}
	return parsePropertyDocument(data, options.IncludeRaw)
}

// ParsePropertyPageWithOptions is an alias for ReadPropertyWithOptions.
func ParsePropertyPageWithOptions(reader io.Reader, options PropertyReaderOptions) (*Property, error) {
	return ReadPropertyWithOptions(reader, options)
}

func parsePropertyDocument(document []byte, includeRaw bool) (*Property, error) {
	return parsePropertyDocumentForZPID(document, includeRaw, "")
}

func parsePropertyDocumentForZPID(document []byte, includeRaw bool, requestedZPID string) (*Property, error) {
	nextData, ok := extractNextData(document)
	if !ok {
		return nil, &SchemaDriftError{
			Operation: "property",
			Path:      "script#__NEXT_DATA__",
			Detail:    "required Next.js data script is missing",
		}
	}

	root, err := decodeJSONValue(nextData)
	if err != nil {
		return nil, &SchemaDriftError{
			Operation: "property",
			Path:      "script#__NEXT_DATA__",
			Detail:    "invalid JSON: " + err.Error(),
		}
	}

	expectedZPID, err := expectedPropertyZPID(root, requestedZPID)
	if err != nil {
		return nil, err
	}

	cache, ok := propertyCache(root)
	if !ok {
		return nil, &SchemaDriftError{
			Operation: "property",
			Path:      "props.pageProps.componentProps.gdpClientCache",
			Detail:    "required property cache is missing",
		}
	}
	cacheValue, err := decodeCache(cache)
	if err != nil {
		return nil, &SchemaDriftError{
			Operation: "property",
			Path:      "props.pageProps.componentProps.gdpClientCache",
			Detail:    "invalid cache JSON: " + err.Error(),
		}
	}

	candidate, property, err := selectPropertyCandidate(cacheValue, expectedZPID)
	if err != nil {
		return nil, err
	}
	if includeRaw {
		raw, err := json.Marshal(candidate.object)
		if err != nil {
			return nil, &SchemaDriftError{
				Operation: "property",
				Path:      candidate.path,
				Detail:    "cannot preserve raw property JSON: " + err.Error(),
			}
		}
		property.Raw = raw
	}
	return &property, nil
}

func expectedPropertyZPID(root any, requestedZPID string) (string, error) {
	expected := requestedZPID
	object, ok := root.(map[string]any)
	if !ok {
		return expected, nil
	}

	paths := [][]string{
		{"props", "pageProps", "componentProps", "zpid"},
		{"props", "pageProps", "componentProps", "property", "zpid"},
		{"props", "pageProps", "zpid"},
		{"props", "pageProps", "query", "zpid"},
		{"query", "zpid"},
	}
	for _, path := range paths {
		value, exists := lookupPath(object, path...)
		if !exists || value == nil {
			continue
		}
		zpid, valid := normalizeZPID(value)
		pathText := strings.Join(path, ".")
		if !valid {
			return "", &SchemaDriftError{
				Operation: "property",
				Path:      pathText,
				Detail:    "authoritative ZPID must be a positive decimal identifier",
			}
		}
		if expected != "" && expected != zpid {
			return "", &SchemaDriftError{
				Operation: "property",
				Path:      pathText,
				Detail:    fmt.Sprintf("authoritative ZPID %s conflicts with expected ZPID %s", zpid, expected),
			}
		}
		expected = zpid
	}
	return expected, nil
}

func extractNextData(document []byte) ([]byte, bool) {
	lower := bytes.ToLower(document)
	searchAt := 0
	for searchAt < len(document) {
		relative := bytes.IndexByte(document[searchAt:], '<')
		if relative < 0 {
			return nil, false
		}
		start := searchAt + relative
		if bytes.HasPrefix(lower[start:], []byte("<!--")) {
			commentEnd := bytes.Index(lower[start+4:], []byte("-->"))
			if commentEnd < 0 {
				return nil, false
			}
			searchAt = start + 4 + commentEnd + 3
			continue
		}

		tagEnd := findTagEnd(document, start+1)
		if tagEnd < 0 {
			return nil, false
		}
		rawTag := strings.TrimSpace(string(document[start+1 : tagEnd]))
		if rawTag == "" || strings.HasPrefix(rawTag, "/") || strings.HasPrefix(rawTag, "!") || strings.HasPrefix(rawTag, "?") {
			searchAt = tagEnd + 1
			continue
		}
		nameEnd := 0
		for nameEnd < len(rawTag) && isAttributeNameByte(rawTag[nameEnd]) {
			nameEnd++
		}
		if nameEnd == 0 || !strings.EqualFold(rawTag[:nameEnd], "script") {
			searchAt = tagEnd + 1
			continue
		}

		bodyStart := tagEnd + 1
		closeRelative := bytes.Index(lower[bodyStart:], []byte("</script"))
		if closeRelative < 0 {
			return nil, false
		}
		bodyEnd := bodyStart + closeRelative
		attributes := parseTagAttributes(rawTag[nameEnd:])
		if attributes["id"] == "__NEXT_DATA__" {
			return bytes.TrimSpace(document[bodyStart:bodyEnd]), true
		}
		closeTagEnd := findTagEnd(document, bodyEnd+len("</script"))
		if closeTagEnd < 0 {
			return nil, false
		}
		searchAt = closeTagEnd + 1
	}
	return nil, false
}

func findTagEnd(document []byte, start int) int {
	var quote byte
	for index := start; index < len(document); index++ {
		current := document[index]
		if quote != 0 {
			if current == quote {
				quote = 0
			}
			continue
		}
		switch current {
		case '\'', '"':
			quote = current
		case '>':
			return index
		}
	}
	return -1
}

func parseTagAttributes(raw string) map[string]string {
	attributes := make(map[string]string)
	for index := 0; index < len(raw); {
		for index < len(raw) && (unicode.IsSpace(rune(raw[index])) || raw[index] == '/') {
			index++
		}
		if index >= len(raw) {
			break
		}

		nameStart := index
		for index < len(raw) && isAttributeNameByte(raw[index]) {
			index++
		}
		if nameStart == index {
			index++
			continue
		}
		name := strings.ToLower(raw[nameStart:index])
		for index < len(raw) && unicode.IsSpace(rune(raw[index])) {
			index++
		}
		if index >= len(raw) || raw[index] != '=' {
			attributes[name] = ""
			continue
		}
		index++
		for index < len(raw) && unicode.IsSpace(rune(raw[index])) {
			index++
		}

		var value string
		if index < len(raw) && (raw[index] == '\'' || raw[index] == '"') {
			quote := raw[index]
			index++
			valueStart := index
			for index < len(raw) && raw[index] != quote {
				index++
			}
			value = raw[valueStart:index]
			if index < len(raw) {
				index++
			}
		} else {
			valueStart := index
			for index < len(raw) && !unicode.IsSpace(rune(raw[index])) && raw[index] != '>' {
				index++
			}
			value = raw[valueStart:index]
		}
		attributes[name] = html.UnescapeString(value)
	}
	return attributes
}

func isAttributeNameByte(value byte) bool {
	return value >= 'a' && value <= 'z' ||
		value >= 'A' && value <= 'Z' ||
		value >= '0' && value <= '9' ||
		value == '-' || value == '_' || value == ':'
}

func decodeJSONValue(data []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, errors.New("multiple JSON values")
		}
		return nil, err
	}
	return value, nil
}

func propertyCache(root any) (any, bool) {
	if object, ok := root.(map[string]any); ok {
		if value, ok := lookupPath(object, "props", "pageProps", "componentProps", "gdpClientCache"); ok {
			return value, true
		}
	}
	return findKeyRecursive(root, "gdpClientCache")
}

func decodeCache(cache any) (any, error) {
	value := cache
	for attempts := 0; attempts < 2; attempts++ {
		text, ok := value.(string)
		if !ok {
			return value, nil
		}
		decoded, err := decodeJSONValue([]byte(text))
		if err != nil {
			return nil, err
		}
		value = decoded
	}
	if _, stillString := value.(string); stillString {
		return nil, errors.New("cache remains a JSON string after two decode passes")
	}
	return value, nil
}

type propertyCandidate struct {
	object   map[string]any
	path     string
	identity propertyIdentity
}

type propertyIdentity struct {
	key  string
	zpid string
	url  string
}

func selectPropertyCandidate(root any, expectedZPID string) (propertyCandidate, Property, error) {
	candidates := make([]propertyCandidate, 0)
	collectPropertyObjects(root, "", &candidates)
	if len(candidates) == 0 {
		return propertyCandidate{}, Property{}, &SchemaDriftError{
			Operation: "property",
			Path:      "gdpClientCache.*.property",
			Detail:    "property object is missing",
		}
	}

	groups := make(map[string][]propertyCandidate)
	var firstIdentityError error
	hasIdentitylessCandidate := false
	for index := range candidates {
		identity, err := inspectPropertyIdentity(candidates[index].object)
		if err != nil {
			if firstIdentityError == nil {
				firstIdentityError = &SchemaDriftError{
					Operation: "property",
					Path:      candidates[index].path,
					Detail:    err.Error(),
				}
			}
			continue
		}
		candidates[index].identity = identity
		if identity.key == "" {
			if expectedZPID == "" {
				hasIdentitylessCandidate = true
			}
			continue
		}
		if expectedZPID != "" {
			if identity.zpid == expectedZPID {
				groups[identity.key] = append(groups[identity.key], candidates[index])
			}
			continue
		}
		groups[identity.key] = append(groups[identity.key], candidates[index])
	}

	if expectedZPID != "" {
		matching := groups["zpid:"+expectedZPID]
		if len(matching) == 0 {
			return propertyCandidate{}, Property{}, &SchemaDriftError{
				Operation: "property",
				Path:      "gdpClientCache.*.property",
				Detail:    fmt.Sprintf("property object for expected ZPID %s is missing", expectedZPID),
			}
		}
		return bestPropertyCandidate(matching)
	}

	if firstIdentityError != nil {
		return propertyCandidate{}, Property{}, firstIdentityError
	}
	if hasIdentitylessCandidate {
		return propertyCandidate{}, Property{}, &SchemaDriftError{
			Operation: "property",
			Path:      "gdpClientCache.*.property",
			Detail:    "property candidate without a core identity makes selection ambiguous",
		}
	}
	if len(groups) == 0 {
		return propertyCandidate{}, Property{}, &SchemaDriftError{
			Operation: "property",
			Path:      "gdpClientCache.*.property",
			Detail:    "property objects have no valid core identity",
		}
	}
	if len(groups) > 1 {
		identities := make([]string, 0, len(groups))
		for identity := range groups {
			identities = append(identities, strings.TrimPrefix(identity, "zpid:"))
		}
		sort.Strings(identities)
		return propertyCandidate{}, Property{}, &SchemaDriftError{
			Operation: "property",
			Path:      "gdpClientCache.*.property",
			Detail:    "multiple distinct property candidates are ambiguous: " + strings.Join(identities, ", "),
		}
	}
	for _, matching := range groups {
		return bestPropertyCandidate(matching)
	}
	panic("unreachable property candidate selection")
}

func bestPropertyCandidate(candidates []propertyCandidate) (propertyCandidate, Property, error) {
	sort.SliceStable(candidates, func(left, right int) bool {
		return candidates[left].path < candidates[right].path
	})

	bestIndex := -1
	bestScore := -1
	var bestProperty Property
	var firstValidationError error
	for index, candidate := range candidates {
		property, err := normalizeProperty(candidate.object, candidate.identity)
		if err != nil {
			if firstValidationError == nil {
				firstValidationError = &SchemaDriftError{
					Operation: "property",
					Path:      candidate.path,
					Detail:    err.Error(),
				}
			}
			continue
		}
		score := propertyScore(property)
		if score > bestScore {
			bestIndex = index
			bestScore = score
			bestProperty = property
		}
	}
	if bestIndex < 0 {
		if firstValidationError != nil {
			return propertyCandidate{}, Property{}, firstValidationError
		}
		return propertyCandidate{}, Property{}, &SchemaDriftError{
			Operation: "property",
			Path:      "gdpClientCache.*.property",
			Detail:    "property object has no valid normalized values",
		}
	}
	return candidates[bestIndex], bestProperty, nil
}

func collectPropertyObjects(root any, path string, candidates *[]propertyCandidate) {
	switch value := root.(type) {
	case map[string]any:
		if propertyValue, exists := value["property"]; exists {
			if propertyObject, ok := propertyValue.(map[string]any); ok {
				propertyPath := "property"
				if path != "" {
					propertyPath = path + ".property"
				}
				*candidates = append(*candidates, propertyCandidate{
					object: propertyObject,
					path:   propertyPath,
				})
			}
		}
		keys := make([]string, 0, len(value))
		for key := range value {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			nextPath := key
			if path != "" {
				nextPath = path + "." + key
			}
			collectPropertyObjects(value[key], nextPath, candidates)
		}
	case []any:
		for index, item := range value {
			collectPropertyObjects(item, fmt.Sprintf("%s[%d]", path, index), candidates)
		}
	}
}

func inspectPropertyIdentity(raw map[string]any) (propertyIdentity, error) {
	identity := propertyIdentity{}
	for _, key := range []string{"zpid", "id"} {
		value, exists := raw[key]
		if !exists || value == nil {
			continue
		}
		zpid, ok := normalizeZPID(value)
		if !ok {
			// GDP property objects commonly use a GraphQL global ID in `id`.
			// It is not a Zillow property identifier, so ignore it and rely on
			// zpid or the canonical property URL instead.
			if key == "id" {
				continue
			}
			return propertyIdentity{}, fmt.Errorf("%s must be a positive decimal identifier", key)
		}
		if identity.zpid != "" && identity.zpid != zpid {
			return propertyIdentity{}, fmt.Errorf("conflicting property identifiers %s and %s", identity.zpid, zpid)
		}
		identity.zpid = zpid
	}

	for _, key := range []string{"url", "hdpUrl", "detailUrl"} {
		value, exists := raw[key]
		if !exists || value == nil {
			continue
		}
		text, ok := value.(string)
		if !ok {
			return propertyIdentity{}, fmt.Errorf("%s must be a string", key)
		}
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		normalized := normalizeZillowURL(text)
		parsed, err := url.Parse(normalized)
		if err != nil || parsed.Path == "" {
			return propertyIdentity{}, fmt.Errorf("%s must be a valid property URL", key)
		}
		urlZPID := zpidFromPath(parsed.Path)
		if urlZPID != "" {
			if identity.zpid != "" && identity.zpid != urlZPID {
				return propertyIdentity{}, fmt.Errorf("%s ZPID %s conflicts with property ZPID %s", key, urlZPID, identity.zpid)
			}
			identity.zpid = urlZPID
		}
		if identity.url == "" {
			identity.url = normalized
		}
	}

	if identity.zpid != "" {
		identity.key = "zpid:" + identity.zpid
	} else if identity.url != "" {
		identity.key = "url:" + identity.url
	}
	return identity, nil
}

func normalizeZPID(value any) (string, bool) {
	text, ok := stringFromAny(value)
	if !ok {
		return "", false
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", false
	}
	for _, character := range text {
		if character < '0' || character > '9' {
			return "", false
		}
	}
	text = strings.TrimLeft(text, "0")
	if text == "" {
		return "", false
	}
	return text, true
}

func zpidFromPath(path string) string {
	var result string
	for _, segment := range strings.Split(path, "/") {
		if !strings.HasSuffix(strings.ToLower(segment), "_zpid") {
			continue
		}
		zpid, ok := normalizeZPID(segment[:len(segment)-len("_zpid")])
		if ok {
			result = zpid
		}
	}
	return result
}

func validatePropertyValues(raw map[string]any) error {
	for _, key := range []string{"price", "unformattedPrice"} {
		if value, exists := raw[key]; exists && value != nil {
			if _, ok := int64FromAny(value); !ok {
				text, isString := value.(string)
				if !isString {
					return fmt.Errorf("%s must be a number or money string", key)
				}
				if _, ok := parseMoney(text); !ok {
					return fmt.Errorf("%s must be a number or money string", key)
				}
			}
		}
	}
	for _, key := range []string{"bedrooms", "beds", "bathrooms", "baths", "lotSize", "lotAreaValue", "latitude", "lat", "longitude", "lng", "lon"} {
		if value, exists := raw[key]; exists && value != nil {
			if _, ok := floatFromAny(value); !ok {
				return fmt.Errorf("%s must be numeric", key)
			}
		}
	}
	for _, key := range []string{"livingArea", "area", "yearBuilt"} {
		if value, exists := raw[key]; exists && value != nil && !validInteger(value) {
			return fmt.Errorf("%s must be an integer", key)
		}
	}
	for _, key := range []string{
		"streetAddress", "addressStreet", "city", "addressCity", "state", "addressState",
		"homeType", "propertyType", "homeStatus", "statusType", "statusText", "description",
		"imgSrc", "imageUrl",
	} {
		if value, exists := raw[key]; exists && value != nil {
			if _, ok := value.(string); !ok {
				return fmt.Errorf("%s must be a string", key)
			}
		}
	}
	for _, key := range []string{"zipcode", "postalCode", "addressZipcode"} {
		if value, exists := raw[key]; exists && value != nil {
			if _, ok := stringFromAny(value); !ok {
				return fmt.Errorf("%s must be a string or number", key)
			}
		}
	}

	if value, exists := raw["address"]; exists && value != nil {
		switch address := value.(type) {
		case string:
		case map[string]any:
			if err := validateAddressValues(address); err != nil {
				return fmt.Errorf("address.%w", err)
			}
		default:
			return errors.New("address must be a string or object")
		}
	}
	if value, exists := raw["latLong"]; exists && value != nil {
		coordinates, ok := value.(map[string]any)
		if !ok {
			return errors.New("latLong must be an object")
		}
		for _, key := range []string{"latitude", "lat", "longitude", "lng", "lon"} {
			if coordinate, exists := coordinates[key]; exists && coordinate != nil {
				if _, ok := floatFromAny(coordinate); !ok {
					return fmt.Errorf("latLong.%s must be numeric", key)
				}
			}
		}
	}
	return nil
}

func validateAddressValues(raw map[string]any) error {
	for _, key := range []string{"streetAddress", "addressStreet", "city", "addressCity", "state", "addressState", "full", "formattedAddress"} {
		if value, exists := raw[key]; exists && value != nil {
			if _, ok := value.(string); !ok {
				return fmt.Errorf("%s must be a string", key)
			}
		}
	}
	for _, key := range []string{"zipcode", "postalCode", "addressZipcode"} {
		if value, exists := raw[key]; exists && value != nil {
			if _, ok := stringFromAny(value); !ok {
				return fmt.Errorf("%s must be a string or number", key)
			}
		}
	}
	return nil
}

func validInteger(value any) bool {
	if _, ok := int64FromAny(value); !ok {
		return false
	}
	switch typed := value.(type) {
	case json.Number:
		parsed, err := typed.Float64()
		return err == nil && math.Trunc(parsed) == parsed
	case float64:
		return math.Trunc(typed) == typed
	case float32:
		return math.Trunc(float64(typed)) == float64(typed)
	default:
		return true
	}
}

func propertyScore(property Property) int {
	score := 0
	if property.ID != "" {
		score += 8
	}
	if property.URL != "" {
		score += 4
	}
	if property.Address.Full != "" {
		score += 4
	}
	if property.Price != nil {
		score += 3
	}
	if property.Bedrooms != nil {
		score += 2
	}
	if property.Bathrooms != nil {
		score += 2
	}
	if property.LivingArea != nil {
		score += 2
	}
	if property.HomeType != "" {
		score++
	}
	if property.Coordinates.Latitude != nil {
		score++
	}
	if property.Coordinates.Longitude != nil {
		score++
	}
	if property.Description != "" {
		score++
	}
	if property.YearBuilt != nil {
		score++
	}
	if property.Laundry != "" && property.Laundry != LaundryUnknown {
		score += 2
	}
	if property.Parking != "" && property.Parking != ParkingUnknown {
		score++
	}
	if property.PetPolicy != "" && property.PetPolicy != PetPolicyUnknown {
		score++
	}
	if len(property.FlexSpaces) > 0 {
		score++
	}
	return score
}

func normalizeProperty(raw map[string]any, identity propertyIdentity) (Property, error) {
	if err := validatePropertyValues(raw); err != nil {
		return Property{}, err
	}
	property := Property{
		ID:          identity.zpid,
		URL:         normalizeZillowURL(firstString(raw, "url", "hdpUrl", "detailUrl")),
		Address:     normalizeAddress(raw),
		Price:       moneyPointer(raw, "unformattedPrice", "baseRent", "minBaseRent", "minPrice", "price"),
		Bedrooms:    floatPointer(raw, "bedrooms", "beds"),
		Bathrooms:   floatPointer(raw, "bathrooms", "baths"),
		LivingArea:  int64Pointer(raw, "livingArea", "area"),
		LotSize:     floatPointer(raw, "lotSize", "lotAreaValue"),
		YearBuilt:   int64Pointer(raw, "yearBuilt"),
		HomeType:    firstString(raw, "homeType", "propertyType"),
		Status:      firstString(raw, "homeStatus", "statusType", "statusText"),
		Description: firstString(raw, "description"),
		ImageURL:    firstString(raw, "imgSrc", "imageUrl", "responsivePhotosOriginalRatio"),
		Coordinates: normalizeCoordinates(raw),
	}
	if property.ID == "" {
		property.ID = firstString(raw, "zpid", "id")
	}
	if property.URL == "" {
		property.URL = identity.url
	}
	if property.ID == "" && property.URL == "" {
		return Property{}, errors.New("normalized property has no core identity")
	}
	property.RequiredMonthlyFees = moneyPointer(raw, "totalRequiredMonthlyMinFee")
	property.PriceIncludesRequiredFees = boolPointer(raw, "listPriceIncludesRequiredMonthlyFees")
	property.TotalMonthlyCost = totalMonthlyCost(property.Price, property.RequiredMonthlyFees, property.PriceIncludesRequiredFees)
	populatePropertyRentalFacts(&property, raw)
	property.SharedHousing, property.StudentHousing = detectSharedAndStudentHousing(raw, property.Description, property.Address.Full)
	property.IncomeRestricted = detectIncomeRestrictedHousing(raw, property.Description)
	if containsExact(property.FlexSpaces, ParkingPrivateGarage) {
		property.VerificationNotes = append(property.VerificationNotes, "Private garage must be verified as exclusive-use, non-shared, and included in rent.")
	}
	return property, nil
}
