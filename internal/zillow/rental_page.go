package zillow

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strings"
)

const (
	RentalPageProperty  = "property"
	RentalPageCommunity = "community"
)

// RentalPage is a Zillow rental page normalized into one or more rentable
// properties. Community pages can expand into units or floor plans.
type RentalPage struct {
	Kind       string     `json:"kind"`
	Properties []Property `json:"properties"`
}

type communityPropertyCandidate struct {
	property Property
	dedupKey string
}

// FetchRentalPage downloads and parses either an individual property page or
// an apartment-community page.
func (c *Client) FetchRentalPage(ctx context.Context, rawURL string) (*RentalPage, error) {
	return c.FetchRentalPageWithOptions(ctx, rawURL, PropertyOptions{})
}

// FetchRentalPageWithOptions downloads and parses a Zillow rental page.
func (c *Client) FetchRentalPageWithOptions(ctx context.Context, rawURL string, options PropertyOptions) (*RentalPage, error) {
	if c == nil {
		return nil, errors.New("fetch Zillow rental page: client is nil")
	}
	target, err := parseAllowedZillowURL(rawURL)
	if err != nil {
		return nil, fmt.Errorf("fetch Zillow rental page: %w", err)
	}
	target.Fragment = ""
	data, _, err := c.execute(ctx, requestSpec{
		operation: "rental page",
		method:    http.MethodGet,
		url:       target,
		kind:      responseHTML,
	})
	if err != nil {
		return nil, err
	}
	page, err := parseRentalPageDocument(data, options.IncludeRaw, zpidFromPath(target.Path), target.String())
	if err != nil {
		return nil, err
	}
	for index := range page.Properties {
		if page.Properties[index].URL == "" {
			page.Properties[index].URL = target.String()
		}
	}
	return page, nil
}

// ReadRentalPage parses an individual or community Zillow rental page.
func ReadRentalPage(reader io.Reader) (*RentalPage, error) {
	return ReadRentalPageWithOptions(reader, PropertyReaderOptions{})
}

// ReadRentalPageWithOptions parses a bounded Zillow rental page.
func ReadRentalPageWithOptions(reader io.Reader, options PropertyReaderOptions) (*RentalPage, error) {
	if reader == nil {
		return nil, errors.New("read Zillow rental page: reader is nil")
	}
	limit := options.MaxBytes
	if limit == 0 {
		limit = DefaultMaxResponseBytes
	}
	if limit < 0 {
		return nil, errors.New("read Zillow rental page: maximum size must be positive")
	}
	data, err := readBounded(reader, limit)
	if err != nil {
		if errors.Is(err, ErrResponseTooLarge) {
			return nil, &ResponseTooLargeError{Limit: limit}
		}
		return nil, fmt.Errorf("read Zillow rental page: %w", err)
	}
	if reason := detectChallenge(responseHTML, http.StatusOK, "text/html", data); reason != "" {
		return nil, &ChallengeError{StatusCode: http.StatusOK, Reason: reason}
	}
	return parseRentalPageDocument(data, options.IncludeRaw, "", "")
}

func parseRentalPageDocument(document []byte, includeRaw bool, requestedZPID, sourceURL string) (*RentalPage, error) {
	nextData, ok := extractNextData(document)
	if !ok {
		return nil, &SchemaDriftError{Operation: "rental page", Path: "script#__NEXT_DATA__", Detail: "required Next.js data script is missing"}
	}
	rootValue, err := decodeJSONValue(nextData)
	if err != nil {
		return nil, &SchemaDriftError{Operation: "rental page", Path: "script#__NEXT_DATA__", Detail: "invalid JSON: " + err.Error()}
	}
	root, ok := rootValue.(map[string]any)
	if !ok {
		return nil, &SchemaDriftError{Operation: "rental page", Path: "script#__NEXT_DATA__", Detail: "top-level value must be an object"}
	}
	if rawBuilding, ok := lookupPath(root, "props", "pageProps", "componentProps", "initialReduxState", "gdp", "building"); ok {
		building, ok := rawBuilding.(map[string]any)
		if !ok {
			return nil, &SchemaDriftError{Operation: "rental page", Path: "props.pageProps.componentProps.initialReduxState.gdp.building", Detail: "building must be an object"}
		}
		properties := normalizeCommunityProperties(building, sourceURL, includeRaw)
		if len(properties) == 0 {
			return nil, &SchemaDriftError{Operation: "rental page", Path: "building.floorPlans", Detail: "no currently described rental units or floor plans"}
		}
		return &RentalPage{Kind: RentalPageCommunity, Properties: properties}, nil
	}

	property, err := parsePropertyDocumentForZPID(document, includeRaw, requestedZPID)
	if err != nil {
		return nil, err
	}
	return &RentalPage{Kind: RentalPageProperty, Properties: []Property{*property}}, nil
}

func normalizeCommunityProperties(building map[string]any, sourceURL string, includeRaw bool) []Property {
	baseAddress := normalizeAddress(building)
	baseDescription := firstString(building, "description")
	baseCoordinates := normalizeCoordinates(building)
	buildingSharedLaundry := false
	if attributes, ok := building["buildingAttributes"].(map[string]any); ok {
		buildingSharedLaundry, _ = boolFromAny(attributes["hasSharedLaundry"])
	}
	commonAmenities := recursiveStrings(building["commonUnitAmenities"])

	candidates := make([]communityPropertyCandidate, 0)
	floorPlans, _ := building["floorPlans"].([]any)
	for floorPlanIndex, rawFloorPlan := range floorPlans {
		floorPlan, ok := rawFloorPlan.(map[string]any)
		if !ok {
			continue
		}
		units, _ := floorPlan["units"].([]any)
		addedUnit := false
		for unitIndex, rawUnit := range units {
			unit, ok := rawUnit.(map[string]any)
			if !ok {
				continue
			}
			property, ok := normalizeCommunityRental(building, floorPlan, unit, baseAddress, baseCoordinates, baseDescription, commonAmenities, buildingSharedLaundry, sourceURL, includeRaw, false)
			if ok {
				candidates = append(candidates, communityPropertyCandidate{
					property: property,
					dedupKey: communityPropertyDedupKey(property.ID, fmt.Sprintf("floorPlans[%d].units[%d]", floorPlanIndex, unitIndex)),
				})
				addedUnit = true
			}
		}
		if !addedUnit {
			property, ok := normalizeCommunityRental(building, floorPlan, nil, baseAddress, baseCoordinates, baseDescription, commonAmenities, buildingSharedLaundry, sourceURL, includeRaw, true)
			if ok {
				candidates = append(candidates, communityPropertyCandidate{
					property: property,
					dedupKey: communityPropertyDedupKey(property.ID, fmt.Sprintf("floorPlans[%d]", floorPlanIndex)),
				})
			}
		}
	}
	if units, ok := building["ungroupedUnits"].([]any); ok {
		for unitIndex, rawUnit := range units {
			unit, ok := rawUnit.(map[string]any)
			if !ok {
				continue
			}
			property, ok := normalizeCommunityRental(building, nil, unit, baseAddress, baseCoordinates, baseDescription, commonAmenities, buildingSharedLaundry, sourceURL, includeRaw, false)
			if ok {
				candidates = append(candidates, communityPropertyCandidate{
					property: property,
					dedupKey: communityPropertyDedupKey(property.ID, fmt.Sprintf("ungroupedUnits[%d]", unitIndex)),
				})
			}
		}
	}
	return deduplicateCommunityProperties(candidates)
}

func normalizeCommunityRental(building, floorPlan, unit map[string]any, baseAddress Address, baseCoordinates Coordinates, baseDescription string, commonAmenities []string, buildingSharedLaundry bool, sourceURL string, includeRaw, floorPlanOnly bool) (Property, bool) {
	value := mergeCommunityValues(floorPlan, unit)
	id := communityZPID(unit)
	if unit == nil {
		id = communityZPID(floorPlan)
	}
	price := firstMoneyPointer(unit, floorPlan, "baseRent", "minBaseRent", "minPrice", "price")
	availability := firstNormalizedAvailability(unit, floorPlan, "availableFrom", "availabilityDate")
	if id == "" && price == nil && availability == "" {
		return Property{}, false
	}

	address := baseAddress
	unitNumber := firstString(unit, "unitNumber")
	if unitNumber != "" {
		address.Street = strings.TrimSpace(address.Street + " #" + unitNumber)
		address.Full = joinAddress(address)
	}
	description := strings.TrimSpace(strings.Join(nonEmpty(firstString(unit, "description", "additionalInformation"), firstString(floorPlan, "description", "additionalInformation"), baseDescription), " | "))
	unitAmenities := append(recursiveStrings(unit["amenityDetails"]), recursiveStrings(unit["unitFeatures"])...)
	floorAmenities := []string(nil)
	if floorPlan != nil {
		floorAmenities = append(recursiveStrings(floorPlan["amenityDetails"]), recursiveStrings(floorPlan["unitFeatures"])...)
	}
	amenities := uniqueStrings(append(append(unitAmenities, floorAmenities...), commonAmenities...))
	laundry := classifyLaundry(uniqueStrings(append(unitAmenities, floorAmenities...)), description)
	if laundry == LaundryUnknown {
		laundry = classifyLaundry(amenities, description)
	}
	if laundry == LaundryUnknown && buildingSharedLaundry {
		laundry = LaundryShared
	}
	parking := classifyParking(uniqueStrings(append(unitAmenities, floorAmenities...)), description)
	reso := map[string]any{"interiorFeatures": amenities}
	flexSpaces := classifyFlexSpaces(description, reso, parking)
	allowedPets := uniqueStrings(append(stringValues(unit["allowedPets"]), stringValues(floorPlanValue(floorPlan, "allowedPets"))...))

	fees := firstMoneyPointer(unit, floorPlan, "totalRequiredMonthlyMinFee")
	includesFees := firstBoolPointer(unit, floorPlan, "listPriceIncludesRequiredMonthlyFees")
	total := totalMonthlyCost(price, fees, includesFees)

	property := Property{
		ID:                        id,
		URL:                       communityPropertyURL(id, sourceURL),
		Address:                   address,
		Price:                     price,
		RequiredMonthlyFees:       fees,
		TotalMonthlyCost:          total,
		PriceIncludesRequiredFees: includesFees,
		Bedrooms:                  firstFloatPointer(unit, floorPlan, "beds", "bedrooms"),
		Bathrooms:                 firstFloatPointer(unit, floorPlan, "baths", "bathrooms"),
		LivingArea:                firstInt64Pointer(unit, floorPlan, "sqft", "livingArea", "area"),
		HomeType:                  "APARTMENT",
		Status:                    firstStringFromSources(unit, floorPlan, "homeStatus", "statusType", "statusText"),
		Description:               description,
		Coordinates:               baseCoordinates,
		Availability:              availability,
		Laundry:                   laundry,
		LaundryFeatures:           amenities,
		Parking:                   parking,
		ParkingFeatures:           uniqueStrings(append(unitAmenities, floorAmenities...)),
		PetPolicy:                 classifyPetPolicy(allowedPets),
		AllowedPets:               allowedPets,
		FlexSpaces:                flexSpaces,
	}
	if containsExact(property.FlexSpaces, ParkingPrivateGarage) {
		property.VerificationNotes = append(property.VerificationNotes, "Private garage must be verified as exclusive-use, non-shared, and included in rent.")
	}
	if floorPlanOnly {
		property.VerificationNotes = append(property.VerificationNotes, "Floor plan availability and exact unit details require verification.")
	}
	buildingShared, buildingStudent := detectSharedAndStudentHousing(building, baseDescription, baseAddress.Full)
	unitShared, unitStudent := detectSharedAndStudentHousing(value, description, address.Full)
	property.SharedHousing = buildingShared || unitShared
	property.StudentHousing = buildingStudent || unitStudent
	property.IncomeRestricted = detectIncomeRestrictedHousing(building, baseDescription) || detectIncomeRestrictedHousing(value, description)
	if property.TotalMonthlyCost == nil {
		property.VerificationNotes = append(property.VerificationNotes, "Required monthly fees and total monthly cost need verification.")
	}
	if includeRaw {
		if encoded, err := json.Marshal(value); err == nil {
			property.Raw = encoded
		}
	}
	return property, true
}

func mergeCommunityValues(floorPlan, unit map[string]any) map[string]any {
	result := cloneMap(floorPlan)
	if result == nil {
		result = make(map[string]any)
	}
	if unit != nil {
		delete(result, "zpid")
	}
	for key, value := range unit {
		if value != nil {
			result[key] = cloneJSONValue(value)
		}
	}
	return result
}

func communityZPID(raw map[string]any) string {
	if raw == nil {
		return ""
	}
	id, ok := normalizeZPID(raw["zpid"])
	if !ok {
		return ""
	}
	return id
}

func floorPlanValue(floorPlan map[string]any, key string) any {
	if floorPlan == nil {
		return nil
	}
	return floorPlan[key]
}

func boolPointer(raw map[string]any, key string) *bool {
	if raw == nil {
		return nil
	}
	value, ok := boolFromAny(raw[key])
	if !ok {
		return nil
	}
	copy := value
	return &copy
}

func firstStringFromSources(primary, fallback map[string]any, keys ...string) string {
	if value := firstString(primary, keys...); value != "" {
		return value
	}
	return firstString(fallback, keys...)
}

func firstMoneyPointer(primary, fallback map[string]any, keys ...string) *int64 {
	if value := moneyPointer(primary, keys...); value != nil {
		return value
	}
	return moneyPointer(fallback, keys...)
}

func firstBoolPointer(primary, fallback map[string]any, key string) *bool {
	if value := boolPointer(primary, key); value != nil {
		return value
	}
	return boolPointer(fallback, key)
}

func firstNormalizedAvailability(primary, fallback map[string]any, keys ...string) string {
	if value := normalizedAvailabilityFromSource(primary, keys...); value != "" {
		return value
	}
	return normalizedAvailabilityFromSource(fallback, keys...)
}

func normalizedAvailabilityFromSource(raw map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := normalizedAvailability(raw[key]); value != "" {
			return value
		}
	}
	return ""
}

func firstFloatPointer(primary, fallback map[string]any, keys ...string) *float64 {
	if value := floatPointer(primary, keys...); value != nil {
		return value
	}
	return floatPointer(fallback, keys...)
}

func firstInt64Pointer(primary, fallback map[string]any, keys ...string) *int64 {
	if value := int64Pointer(primary, keys...); value != nil {
		return value
	}
	return int64Pointer(fallback, keys...)
}

func totalMonthlyCost(price, fees *int64, includesFees *bool) *int64 {
	if price == nil {
		return nil
	}
	if includesFees != nil && *includesFees {
		value := *price
		return &value
	}
	if fees == nil || *price < 0 || *fees < 0 || *fees > math.MaxInt64-*price {
		return nil
	}
	value := *price + *fees
	return &value
}

func communityPropertyURL(id, fallback string) string {
	if _, ok := normalizeZPID(id); ok {
		return "https://www.zillow.com/homedetails/" + id + "_zpid/"
	}
	if parsed, err := url.Parse(fallback); err == nil && parsed.Scheme == "https" && parsed.Host != "" {
		return parsed.String()
	}
	return ""
}

func communityPropertyDedupKey(id, sourcePath string) string {
	if id != "" {
		return "zpid:" + id
	}
	// Source paths keep ZPID-less floor plans and units distinct without
	// exposing an internal identity as a Zillow property ID.
	return "source:" + sourcePath
}

func deduplicateCommunityProperties(candidates []communityPropertyCandidate) []Property {
	result := make([]Property, 0, len(candidates))
	seen := make(map[string]struct{})
	for _, candidate := range candidates {
		key := candidate.dedupKey
		if key == "" {
			key = candidate.property.URL + "|" + candidate.property.Address.Full
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, candidate.property)
	}
	return result
}

func containsExact(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
