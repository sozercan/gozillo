package zillow

import "testing"

func TestDetectSharedAndStudentHousingSignals(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		raw         map[string]any
		description string
		address     string
		shared      bool
		student     bool
	}{
		{name: "room flag", raw: map[string]any{"listing_sub_type": map[string]any{"is_roomForRent": true}}, shared: true},
		{name: "student flag", raw: map[string]any{"isStudentHousing": true}, student: true},
		{name: "student unit type", raw: map[string]any{"studentHousingType": "INDIVIDUAL_LEASE"}, shared: true, student: true},
		{name: "per bed description", description: "Individual leases priced per bed", shared: true},
		{name: "co living", description: "Modern co-living community", shared: true},
		{name: "room unit identifier", address: "100 Example Ave #Unit 203RM3", shared: true},
		{name: "ordinary apartment", description: "Entire apartment for rent", address: "100 Example Ave #203"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			shared, student := detectSharedAndStudentHousing(test.raw, test.description, test.address)
			if shared != test.shared || student != test.student {
				t.Fatalf("got shared=%t student=%t, want shared=%t student=%t", shared, student, test.shared, test.student)
			}
		})
	}
}

func TestDetectIncomeRestrictedHousingSignals(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		raw         map[string]any
		description string
		want        bool
	}{
		{name: "direct flag", raw: map[string]any{"isIncomeRestricted": true}, want: true},
		{name: "low income flag", raw: map[string]any{"isLowIncome": true}, want: true},
		{name: "building limits", raw: map[string]any{"building": map[string]any{"buildingAttributes": map[string]any{"incomeRestrictions": map[string]any{"incomeLimits": []any{map[string]any{"maximumIncome": 70000}}}}}}, want: true},
		{name: "description", description: "Income restricted apartment with household income limits", want: true},
		{name: "ordinary", description: "Standard market-rate apartment"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := detectIncomeRestrictedHousing(test.raw, test.description); got != test.want {
				t.Fatalf("detectIncomeRestrictedHousing() = %t, want %t", got, test.want)
			}
		})
	}
}
