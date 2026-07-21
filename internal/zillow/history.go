package zillow

import (
	"sort"
	"strings"
)

const (
	HistoryNewToDigest           = "new-to-digest"
	HistoryPreviouslyChanged     = "previously-surfaced-changed"
	HistoryPreviouslyStillActive = "previously-surfaced-still-active"
)

// AnnotateListingHistory returns current listings labeled against a previous
// result set. Matching prefers Zillow ID, then URL, then normalized address.
func AnnotateListingHistory(current, previous []Listing) []Listing {
	index := make(map[string]Listing, len(previous)*2)
	for _, listing := range previous {
		for _, key := range listingHistoryKeys(listing) {
			if _, exists := index[key]; !exists {
				index[key] = listing
			}
		}
	}

	result := append([]Listing(nil), current...)
	for i := range result {
		result[i].HistoryChanges = nil
		var prior Listing
		found := false
		for _, key := range listingHistoryKeys(result[i]) {
			if candidate, ok := index[key]; ok {
				prior = candidate
				found = true
				break
			}
		}
		if !found {
			result[i].HistoryStatus = HistoryNewToDigest
			continue
		}
		result[i].HistoryChanges = meaningfulListingChanges(prior, result[i])
		if len(result[i].HistoryChanges) == 0 {
			result[i].HistoryStatus = HistoryPreviouslyStillActive
		} else {
			result[i].HistoryStatus = HistoryPreviouslyChanged
		}
	}
	return result
}

func listingHistoryKeys(listing Listing) []string {
	keys := make([]string, 0, 3)
	if id := strings.TrimSpace(listing.ID); id != "" {
		keys = append(keys, "id:"+id)
	}
	if rawURL := strings.TrimSpace(listing.URL); rawURL != "" {
		keys = append(keys, "url:"+rawURL)
	}
	if address := strings.ToLower(strings.Join(strings.Fields(listing.Address.Full), " ")); address != "" {
		keys = append(keys, "address:"+address)
	}
	return keys
}

func meaningfulListingChanges(previous, current Listing) []string {
	changes := make([]string, 0, 8)
	changes = appendMoneyChange(changes, "price", previous.Price, current.Price)
	changes = appendMoneyChange(changes, "total monthly cost", previous.TotalMonthlyCost, current.TotalMonthlyCost)
	changes = appendMoneyChange(changes, "required monthly fees", previous.RequiredMonthlyFees, current.RequiredMonthlyFees)
	if strings.TrimSpace(previous.Availability) != strings.TrimSpace(current.Availability) {
		changes = append(changes, "availability changed")
	}
	if !equalFloatPointers(previous.Bedrooms, current.Bedrooms) {
		changes = append(changes, "bedroom count changed")
	}
	if !equalFloatPointers(previous.Bathrooms, current.Bathrooms) {
		changes = append(changes, "bathroom count changed")
	}
	previousLaundry := normalizeHistoryValue(previous.Laundry)
	currentLaundry := normalizeHistoryValue(current.Laundry)
	if previousLaundry != currentLaundry {
		if currentLaundry == LaundryInUnit && previousLaundry != LaundryInUnit {
			changes = append(changes, "in-unit laundry confirmed")
		} else {
			changes = append(changes, "laundry details changed")
		}
	}
	if !equalStringSets(previous.FlexSpaces, current.FlexSpaces) {
		changes = append(changes, "flex details changed")
	}
	if normalizeHistoryValue(previous.Status) != normalizeHistoryValue(current.Status) {
		changes = append(changes, "rental status changed")
	}
	return changes
}

func appendMoneyChange(changes []string, label string, previous, current *int64) []string {
	if previous == nil && current == nil {
		return changes
	}
	if previous == nil && current != nil {
		return append(changes, label+" confirmed")
	}
	if previous != nil && current == nil {
		return append(changes, label+" became unknown")
	}
	if *current < *previous {
		return append(changes, label+" decreased")
	}
	if *current > *previous {
		return append(changes, label+" increased")
	}
	return changes
}

func equalFloatPointers(left, right *float64) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func equalStringSets(left, right []string) bool {
	leftCopy := normalizedStringSet(left)
	rightCopy := normalizedStringSet(right)
	if len(leftCopy) != len(rightCopy) {
		return false
	}
	for index := range leftCopy {
		if leftCopy[index] != rightCopy[index] {
			return false
		}
	}
	return true
}

func normalizedStringSet(values []string) []string {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		value = normalizeHistoryValue(value)
		if value != "" {
			set[value] = struct{}{}
		}
	}
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func normalizeHistoryValue(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}
