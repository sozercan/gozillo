package zillow

import (
	"regexp"
	"strings"
)

var (
	sharedHousingTextPattern  = regexp.MustCompile(`\b(?:room for rent|private room|shared room|shared housing|shared apartment|rent by the room|individual lease|individual leasing|per person|per bed|bed space|bedspace|co[- ]?living|coliving|communal living)\b`)
	studentHousingTextPattern = regexp.MustCompile(`\b(?:student housing|student apartment|campus housing|university housing|dorm|dormitory|residence hall)\b`)
	roomUnitPattern           = regexp.MustCompile(`\b(?:unit )?[0-9]+rm[0-9]+\b|\bbedroom [a-z0-9]+\b`)
)

func detectSharedAndStudentHousing(raw map[string]any, description, address string) (bool, bool) {
	shared := false
	student := false
	for _, key := range []string{"roomForRent", "isRoomForRent"} {
		if value, ok := boolFromAny(raw[key]); ok && value {
			shared = true
		}
	}
	if listingSubtype, ok := raw["listing_sub_type"].(map[string]any); ok {
		if value, ok := boolFromAny(listingSubtype["is_roomForRent"]); ok && value {
			shared = true
		}
	}
	if value, ok := boolFromAny(raw["isStudentHousing"]); ok && value {
		student = true
	}
	for _, key := range []string{"studentHousingType", "communityType", "communitySubType", "buildingType"} {
		value := canonicalText(firstString(raw, key))
		if value == "" || value == "none" || value == "not student housing" {
			continue
		}
		if strings.Contains(value, "student") || strings.Contains(value, "individual lease") || strings.Contains(value, "per bed") {
			student = true
		}
		if strings.Contains(value, "individual lease") || strings.Contains(value, "per bed") {
			shared = true
		}
	}
	if units, ok := raw["units"].([]any); ok {
		for _, item := range units {
			unit, ok := item.(map[string]any)
			if !ok {
				continue
			}
			if value, ok := boolFromAny(unit["roomForRent"]); ok && value {
				shared = true
			}
			unitType := canonicalText(firstString(unit, "studentHousingType"))
			if unitType != "" && unitType != "none" {
				student = true
				if strings.Contains(unitType, "individual") || strings.Contains(unitType, "bed") {
					shared = true
				}
			}
		}
	}
	text := canonicalText(strings.Join([]string{description, address}, " | "))
	if sharedHousingTextPattern.MatchString(text) || roomUnitPattern.MatchString(text) {
		shared = true
	}
	if studentHousingTextPattern.MatchString(text) {
		student = true
	}
	return shared, student
}

var incomeRestrictedTextPattern = regexp.MustCompile(`\b(?:income restricted|income-restricted|income limit|income limits|maximum income|affordable housing|below market rate|bmr housing|area median income|ami limit|ami limits)\b`)

func detectIncomeRestrictedHousing(raw map[string]any, description string) bool {
	for _, key := range []string{"isIncomeRestricted", "isLowIncome", "showIncomeRestrictedCTA"} {
		if value, ok := boolFromAny(raw[key]); ok && value {
			return true
		}
	}
	if hasIncomeRestrictionObject(raw["incomeRestrictions"]) {
		return true
	}
	if attributes, ok := raw["buildingAttributes"].(map[string]any); ok {
		if hasIncomeRestrictionObject(attributes["incomeRestrictions"]) {
			return true
		}
	}
	if building, ok := raw["building"].(map[string]any); ok {
		for _, key := range []string{"isIncomeRestricted", "isLowIncome", "showIncomeRestrictedCTA"} {
			if value, ok := boolFromAny(building[key]); ok && value {
				return true
			}
		}
		if attributes, ok := building["buildingAttributes"].(map[string]any); ok && hasIncomeRestrictionObject(attributes["incomeRestrictions"]) {
			return true
		}
	}
	return incomeRestrictedTextPattern.MatchString(canonicalText(description))
}

func hasIncomeRestrictionObject(value any) bool {
	switch typed := value.(type) {
	case map[string]any:
		if len(typed) == 0 {
			return false
		}
		if limits, ok := typed["incomeLimits"].([]any); ok {
			return len(limits) > 0
		}
		return true
	case []any:
		return len(typed) > 0
	default:
		return false
	}
}
