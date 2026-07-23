package zillow

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	LaundryUnknown = "unknown"
	LaundryInUnit  = "in-unit"
	LaundryHookups = "hookups"
	LaundryShared  = "shared"
	LaundryNone    = "none"

	ParkingUnknown       = "unknown"
	ParkingAvailable     = "available"
	ParkingGarage        = "garage"
	ParkingPrivateGarage = "private-garage"
	ParkingNone          = "none"

	PetPolicyUnknown    = "unknown"
	PetPolicyAllowed    = "allowed"
	PetPolicyRestricted = "restricted"
	PetPolicyNotAllowed = "not-allowed"

	DetailStatusEnriched    = "enriched"
	DetailStatusUnavailable = "unavailable"

	MatchStatusMatch     = "match"
	MatchStatusWatchlist = "watchlist"
)

var (
	spacePattern = regexp.MustCompile(`\s+`)

	flexDescriptionPatterns = map[string][]*regexp.Regexp{
		"den": {
			regexp.MustCompile(`\bden\b`),
		},
		"office": {
			regexp.MustCompile(`\b(?:home|private|dedicated|separate) office\b`),
			regexp.MustCompile(`\boffice (?:space|room|area)\b`),
			regexp.MustCompile(`\b(?:office den|den office)\b`),
			regexp.MustCompile(`\buse(?:d)? as (?:an? )?office\b`),
		},
		"bonus": {
			regexp.MustCompile(`\bbonus (?:room|space|area)\b`),
		},
		"loft": {
			regexp.MustCompile(`\b(?:upstairs |open )?loft (?:room|space|area)\b`),
			regexp.MustCompile(`\b(?:upstairs|open) loft\b`),
		},
		"flex": {
			regexp.MustCompile(`\bflex (?:room|space|area)\b`),
		},
	}
)

func populatePropertyRentalFacts(property *Property, raw map[string]any) {
	if property == nil {
		return
	}

	reso, _ := raw["resoFacts"].(map[string]any)
	atAGlance := atAGlanceFacts(reso)

	laundryFeatures := append([]string{}, stringValues(reso["laundryFeatures"])...)
	if value := atAGlance["laundry"]; value != "" {
		laundryFeatures = append(laundryFeatures, value)
	}
	for _, appliance := range stringValues(reso["appliances"]) {
		if isLaundryAppliance(appliance) {
			laundryFeatures = append(laundryFeatures, appliance)
		}
	}
	property.LaundryFeatures = uniqueStrings(laundryFeatures)
	property.Laundry = classifyLaundry(property.LaundryFeatures, property.Description)

	parkingFeatures := append([]string{}, stringValues(reso["parkingFeatures"])...)
	parkingFeatures = append(parkingFeatures, stringValues(reso["otherParking"])...)
	if value := atAGlance["parking"]; value != "" {
		parkingFeatures = append(parkingFeatures, value)
	}
	if booleanValue(reso["hasAttachedGarage"]) {
		parkingFeatures = append(parkingFeatures, "Attached Garage")
	}
	if booleanValue(reso["hasGarage"]) {
		parkingFeatures = append(parkingFeatures, "Garage")
	}
	if booleanValue(reso["hasOpenParking"]) {
		parkingFeatures = append(parkingFeatures, "Open Parking")
	}
	property.ParkingFeatures = uniqueStrings(parkingFeatures)
	property.Parking = classifyParking(property.ParkingFeatures, property.Description)

	allowedPets := append([]string{}, stringValues(reso["allowedPets"])...)
	if value := atAGlance["pets"]; value != "" {
		allowedPets = append(allowedPets, value)
	}
	if booleanValue(reso["hasPetsAllowed"]) {
		allowedPets = append(allowedPets, "Pets allowed")
	}
	property.AllowedPets = uniqueStrings(allowedPets)
	property.PetPolicy = classifyPetPolicy(property.AllowedPets)

	property.FlexSpaces = classifyFlexSpaces(property.Description, reso, property.Parking)
	property.DaysOnZillow = int64Pointer(raw, "daysOnZillow")
	listedDate, updatedDate, derivedDays := rentalHistoryRecency(raw, time.Now().UTC())
	property.ListedDate = listedDate
	property.UpdatedDate = updatedDate
	if property.DaysOnZillow == nil {
		property.DaysOnZillow = derivedDays
	}
	property.Availability = normalizedAvailability(firstNonNil(
		reso["availabilityDate"],
		raw["availabilityDate"],
		atAGlance["date available"],
	))
	if property.YearBuilt == nil {
		property.YearBuilt = int64Pointer(reso, "yearBuilt", "yearBuiltEffective")
	}
}

func atAGlanceFacts(reso map[string]any) map[string]string {
	facts := make(map[string]string)
	if reso == nil {
		return facts
	}
	items, ok := reso["atAGlanceFacts"].([]any)
	if !ok {
		return facts
	}
	for _, item := range items {
		object, ok := item.(map[string]any)
		if !ok {
			continue
		}
		label, _ := object["factLabel"].(string)
		value, _ := object["factValue"].(string)
		label = strings.ToLower(strings.TrimSpace(label))
		value = strings.TrimSpace(value)
		if label != "" && value != "" {
			facts[label] = value
		}
	}
	return facts
}

func isLaundryAppliance(value string) bool {
	text := canonicalText(value)
	if text == "" {
		return false
	}
	if strings.Contains(text, "dishwasher") {
		text = strings.ReplaceAll(text, "dishwasher", "")
	}
	return containsWord(text, "washer") || containsWord(text, "dryer") || containsAny(text, "laundry", "wd hookup", "w d hookup")
}

func classifyLaundry(features []string, description string) string {
	structured := canonicalText(strings.Join(features, " | "))
	combined := canonicalText(strings.Join(append(append([]string{}, features...), description), " | "))

	if containsAny(structured, "hookup", "hook up", "connections") || containsAny(combined, "washer dryer hookups", "washer and dryer hookups", "w d hookups") {
		return LaundryHookups
	}
	if containsAny(combined, "shared laundry", "community laundry", "common laundry", "coin laundry", "coin operated", "on site laundry") {
		return LaundryShared
	}
	if containsAny(combined, "no laundry", "no washer", "laundry not available") {
		return LaundryNone
	}
	if containsAny(combined,
		"in unit laundry", "in unit washer", "in unit dryer", "washer dryer in unit", "washer and dryer in unit",
		"in home laundry", "in home washer", "in home dryer",
		"private laundry", "private washer", "inside laundry", "washer dryer included", "washer and dryer included",
	) || containsAny(structured, "in unit") {
		return LaundryInUnit
	}
	if containsAny(structured, "washer", "dryer") && strings.Contains(structured, "washer") && strings.Contains(structured, "dryer") {
		return LaundryInUnit
	}
	return LaundryUnknown
}

func classifyParking(features []string, description string) string {
	structured := canonicalText(strings.Join(features, " | "))
	combined := canonicalText(strings.Join(append(append([]string{}, features...), description), " | "))

	if containsAny(combined, "no parking", "parking not available") {
		return ParkingNone
	}
	sharedGarage := containsAny(combined, "shared garage", "community garage", "common garage", "assigned stall", "assigned space in garage")
	if !sharedGarage && containsAny(combined,
		"attached garage", "detached garage", "private garage", "exclusive use garage", "exclusive garage",
		"standalone garage", "individual garage", "enclosed garage",
	) {
		return ParkingPrivateGarage
	}
	if strings.Contains(structured, "garage") || containsAny(combined, "garage parking", "parking garage", "shared garage", "community garage", "common garage") {
		return ParkingGarage
	}
	if containsAny(structured, "parking", "carport", "covered", "assigned", "off street", "driveway", "open") {
		return ParkingAvailable
	}
	return ParkingUnknown
}

func classifyPetPolicy(allowedPets []string) string {
	text := canonicalText(strings.Join(allowedPets, " | "))
	if text == "" {
		return PetPolicyUnknown
	}
	if containsAny(text, "no pets", "pets not allowed", "no animals") {
		return PetPolicyNotAllowed
	}
	if containsAny(text, "restrictions", "restricted", "breed", "weight limit", "deposit", "fee") {
		return PetPolicyRestricted
	}
	if containsAny(text, "pets allowed", "cats", "dogs", "small pets", "large pets", "all pets") {
		genericPermission := containsAny(text, "pets allowed", "all pets")
		speciesSpecific := containsAny(text, "cats", "dogs", "small pets", "large pets")
		if containsAny(text, "cats only", "dogs only", "small dogs") || speciesSpecific && !genericPermission {
			return PetPolicyRestricted
		}
		return PetPolicyAllowed
	}
	return PetPolicyUnknown
}

func classifyFlexSpaces(description string, reso map[string]any, parking string) []string {
	structuredParts := make([]string, 0)
	for _, key := range []string{"rooms", "interiorFeatures", "otherInteriorFeatures"} {
		structuredParts = append(structuredParts, recursiveStrings(reso[key])...)
	}
	structured := canonicalText(strings.Join(structuredParts, " | "))
	description = canonicalText(description)

	matches := make(map[string]bool)
	for _, name := range []string{"den", "office", "bonus", "loft", "flex"} {
		if structuredFlexMatch(name, structured) {
			matches[name] = true
			continue
		}
		for _, pattern := range flexDescriptionPatterns[name] {
			if pattern.MatchString(description) {
				matches[name] = true
				break
			}
		}
	}
	if parking == ParkingPrivateGarage {
		matches[ParkingPrivateGarage] = true
	}

	ordered := []string{"den", "office", "bonus", "loft", "flex", ParkingPrivateGarage}
	result := make([]string, 0, len(matches))
	for _, value := range ordered {
		if matches[value] {
			result = append(result, value)
		}
	}
	return result
}

func structuredFlexMatch(name, text string) bool {
	if text == "" {
		return false
	}
	switch name {
	case "bonus":
		return containsAny(text, "bonus room", "bonus space", "bonus area")
	case "flex":
		return containsAny(text, "flex room", "flex space", "flex area")
	case "office":
		return containsWord(text, "office")
	case "loft":
		return containsWord(text, "loft")
	default:
		return containsWord(text, name)
	}
}

type rentalHistoryEvent struct {
	date   time.Time
	event  string
	rental bool
}

func rentalHistoryRecency(raw map[string]any, today time.Time) (string, string, *int64) {
	items, ok := raw["priceHistory"].([]any)
	if !ok || len(items) == 0 {
		return "", "", nil
	}
	events := make([]rentalHistoryEvent, 0, len(items))
	for _, item := range items {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		parsed, ok := parseRentalHistoryDate(entry["date"])
		if !ok {
			continue
		}
		event := canonicalText(firstString(entry, "event"))
		rental, _ := boolFromAny(entry["postingIsRental"])
		if !rental && !strings.Contains(event, "rent") && !isRentalHistoryLifecycleReset(event) {
			continue
		}
		events = append(events, rentalHistoryEvent{date: parsed, event: event, rental: rental})
	}
	if len(events) == 0 {
		return "", "", nil
	}
	sort.Slice(events, func(i, j int) bool { return events[i].date.Before(events[j].date) })
	var listed time.Time
	var updated time.Time
	for _, event := range events {
		switch {
		case isRentalHistoryLifecycleReset(event.event):
			listed = time.Time{}
			updated = time.Time{}
		case containsAny(event.event, "listed for rent", "listed for rental", "rental listing"):
			listed = event.date
			updated = event.date
		case !listed.IsZero() && event.date.After(updated):
			updated = event.date
		}
	}
	if listed.IsZero() {
		return "", "", nil
	}
	if updated.IsZero() {
		updated = listed
	}
	calendarToday := time.Date(today.Year(), today.Month(), today.Day(), 0, 0, 0, 0, time.UTC)
	calendarListed := time.Date(listed.Year(), listed.Month(), listed.Day(), 0, 0, 0, 0, time.UTC)
	difference := int64(calendarToday.Sub(calendarListed) / (24 * time.Hour))
	if difference < 0 {
		return listed.Format("2006-01-02"), updated.Format("2006-01-02"), nil
	}
	return listed.Format("2006-01-02"), updated.Format("2006-01-02"), &difference
}

func isRentalHistoryLifecycleReset(event string) bool {
	return containsAny(event, "listing removed", "removed listing", "off market", "sold", "pending sale")
}

func parseRentalHistoryDate(value any) (time.Time, bool) {
	text, ok := stringFromAny(value)
	if !ok || text == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02", "Jan 2, 2006", "January 2, 2006"} {
		if parsed, err := time.Parse(layout, text); err == nil {
			return parsed, true
		}
	}
	return time.Time{}, false
}

func normalizedAvailability(value any) string {
	if value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		text = strings.TrimSpace(text)
		if text == "" {
			return ""
		}
		if integer, err := strconv.ParseInt(text, 10, 64); err == nil {
			return unixDate(integer)
		}
		for _, layout := range []string{time.RFC3339, "2006-01-02", "2006-01-02 15:04:05", "Jan 2, 2006", "January 2, 2006"} {
			if parsed, err := time.Parse(layout, text); err == nil {
				return parsed.Format("2006-01-02")
			}
		}
		return text
	}
	if integer, ok := int64FromAny(value); ok {
		return unixDate(integer)
	}
	return ""
}

func unixDate(value int64) string {
	if value <= 0 {
		return ""
	}
	seconds := value
	if value > 100_000_000_000 {
		seconds = value / 1000
	}
	return time.Unix(seconds, 0).UTC().Format("2006-01-02")
}

func stringValues(value any) []string {
	switch typed := value.(type) {
	case string:
		if value := strings.TrimSpace(typed); value != "" {
			return []string{value}
		}
	case []string:
		return uniqueStrings(typed)
	case []any:
		values := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				values = append(values, text)
			}
		}
		return uniqueStrings(values)
	}
	return nil
}

func recursiveStrings(value any) []string {
	values := make([]string, 0)
	var walk func(any)
	walk = func(current any) {
		switch typed := current.(type) {
		case string:
			if text := strings.TrimSpace(typed); text != "" {
				values = append(values, text)
			}
		case []string:
			for _, item := range typed {
				walk(item)
			}
		case []any:
			for _, item := range typed {
				walk(item)
			}
		case map[string]any:
			for key, item := range typed {
				if containsAnyFold(key, "room", "feature", "description", "type", "name") {
					walk(item)
				}
			}
		}
	}
	walk(value)
	return uniqueStrings(values)
}

func uniqueStrings(values []string) []string {
	result := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		key := strings.ToLower(value)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, value)
	}
	return result
}

func canonicalText(value string) string {
	var normalized strings.Builder
	normalized.Grow(len(value))
	for _, character := range strings.ToLower(value) {
		if unicode.IsLetter(character) || unicode.IsNumber(character) {
			normalized.WriteRune(character)
		} else {
			normalized.WriteByte(' ')
		}
	}
	return strings.TrimSpace(spacePattern.ReplaceAllString(normalized.String(), " "))
}

func containsAny(value string, terms ...string) bool {
	for _, term := range terms {
		if strings.Contains(value, term) {
			return true
		}
	}
	return false
}

func containsAnyFold(value string, terms ...string) bool {
	return containsAny(strings.ToLower(value), terms...)
}

func containsWord(value, word string) bool {
	for _, field := range strings.Fields(value) {
		if field == word {
			return true
		}
	}
	return false
}

func booleanValue(value any) bool {
	boolean, ok := value.(bool)
	return ok && boolean
}

func firstNonNil(values ...any) any {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}
