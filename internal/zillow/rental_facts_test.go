package zillow

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestPopulatePropertyRentalFacts(t *testing.T) {
	t.Parallel()

	available := time.Date(2026, time.August, 15, 0, 0, 0, 0, time.UTC).UnixMilli()
	property := Property{Description: "A dedicated home office and private outdoor area."}
	raw := map[string]any{
		"daysOnZillow": 4,
		"resoFacts": map[string]any{
			"availabilityDate":  available,
			"yearBuilt":         2008,
			"laundryFeatures":   []any{"In Unit", "Washer", "Dryer"},
			"appliances":        []any{"Dishwasher", "Refrigerator"},
			"parkingFeatures":   []any{"Attached"},
			"hasAttachedGarage": true,
			"allowedPets":       []any{"Cats allowed", "Small dogs allowed"},
			"rooms": []any{
				map[string]any{"roomType": "Bonus Room"},
			},
		},
	}

	populatePropertyRentalFacts(&property, raw)

	if property.Laundry != LaundryInUnit {
		t.Fatalf("Laundry = %q, want %q", property.Laundry, LaundryInUnit)
	}
	if !reflect.DeepEqual(property.LaundryFeatures, []string{"In Unit", "Washer", "Dryer"}) {
		t.Fatalf("LaundryFeatures = %#v", property.LaundryFeatures)
	}
	if property.Parking != ParkingPrivateGarage {
		t.Fatalf("Parking = %q, want %q", property.Parking, ParkingPrivateGarage)
	}
	if property.PetPolicy != PetPolicyRestricted {
		t.Fatalf("PetPolicy = %q, want %q", property.PetPolicy, PetPolicyRestricted)
	}
	if !reflect.DeepEqual(property.FlexSpaces, []string{"office", "bonus", ParkingPrivateGarage}) {
		t.Fatalf("FlexSpaces = %#v", property.FlexSpaces)
	}
	if property.Availability != "2026-08-15" {
		t.Fatalf("Availability = %q", property.Availability)
	}
	if property.DaysOnZillow == nil || *property.DaysOnZillow != 4 {
		t.Fatalf("DaysOnZillow = %v", property.DaysOnZillow)
	}
	if property.YearBuilt == nil || *property.YearBuilt != 2008 {
		t.Fatalf("YearBuilt = %v", property.YearBuilt)
	}
}

func TestClassifyLaundry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		features    []string
		description string
		want        string
	}{
		{name: "in unit structured", features: []string{"In Unit", "Washer", "Dryer"}, want: LaundryInUnit},
		{name: "in unit structured alone", features: []string{"In Unit"}, want: LaundryInUnit},
		{name: "in unit description", description: "Private in-unit washer and dryer included.", want: LaundryInUnit},
		{name: "in home description", description: "Includes an in-home washer and dryer.", want: LaundryInUnit},
		{name: "hookups", features: []string{"Washer/Dryer Hookups"}, want: LaundryHookups},
		{name: "shared", description: "Coin-operated shared laundry room on site.", want: LaundryShared},
		{name: "unspecified laundry room stays unknown", features: []string{"Laundry Room"}, want: LaundryUnknown},
		{name: "none", description: "No laundry available.", want: LaundryNone},
		{name: "dishwasher is not laundry", features: []string{"Dishwasher"}, want: LaundryUnknown},
		{name: "unknown", want: LaundryUnknown},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := classifyLaundry(test.features, test.description); got != test.want {
				t.Fatalf("classifyLaundry() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestClassifyPetPolicyTreatsSpeciesSpecificPermissionAsRestricted(t *testing.T) {
	t.Parallel()

	if got := classifyPetPolicy([]string{"Cats allowed"}); got != PetPolicyRestricted {
		t.Fatalf("classifyPetPolicy() = %q, want %q", got, PetPolicyRestricted)
	}
	if got := classifyPetPolicy([]string{"Pets allowed"}); got != PetPolicyAllowed {
		t.Fatalf("classifyPetPolicy() = %q, want %q", got, PetPolicyAllowed)
	}
}

func TestClassifyParkingDoesNotTreatSharedGarageAsPrivate(t *testing.T) {
	t.Parallel()

	if got := classifyParking([]string{"Attached Garage"}, "Assigned stall in a shared garage."); got != ParkingGarage {
		t.Fatalf("classifyParking() = %q, want %q", got, ParkingGarage)
	}
	if got := classifyParking([]string{"Attached Garage"}, ""); got != ParkingPrivateGarage {
		t.Fatalf("classifyParking() = %q, want %q", got, ParkingPrivateGarage)
	}
}

func TestClassifyFlexSpacesIsConservativeAboutOfficeMentions(t *testing.T) {
	t.Parallel()

	if got := classifyFlexSpaces("Contact the leasing office during office hours.", nil, ParkingUnknown); len(got) != 0 {
		t.Fatalf("leasing-office description produced flex spaces: %#v", got)
	}
	got := classifyFlexSpaces("Separate home office plus an upstairs loft space.", nil, ParkingPrivateGarage)
	want := []string{"office", "loft", ParkingPrivateGarage}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("classifyFlexSpaces() = %#v, want %#v", got, want)
	}
}

func TestNormalizedAvailability(t *testing.T) {
	t.Parallel()

	milliseconds := time.Date(2026, time.July, 31, 0, 0, 0, 0, time.UTC).UnixMilli()
	for _, test := range []struct {
		value any
		want  string
	}{
		{value: milliseconds, want: "2026-07-31"},
		{value: "2026-08-01T12:00:00Z", want: "2026-08-01"},
		{value: "2026-09-01 00:00:00", want: "2026-09-01"},
		{value: "Available Now", want: "Available Now"},
	} {
		if got := normalizedAvailability(test.value); got != test.want {
			t.Fatalf("normalizedAvailability(%v) = %q, want %q", test.value, got, test.want)
		}
	}
}

func TestReadPropertyNormalizesStructuredRentalFacts(t *testing.T) {
	t.Parallel()

	document := propertyDocument(t, true, map[string]any{
		"query": map[string]any{
			"property": map[string]any{
				"zpid":        123,
				"description": "A separate home office.",
				"resoFacts": map[string]any{
					"laundryFeatures": []any{"In Unit", "Washer", "Dryer"},
					"parkingFeatures": []any{"Detached Garage"},
					"allowedPets":     []any{"No Pets"},
				},
			},
		},
	})
	property, err := ReadProperty(strings.NewReader(document))
	if err != nil {
		t.Fatalf("ReadProperty() error = %v", err)
	}
	if property.Laundry != LaundryInUnit || property.Parking != ParkingPrivateGarage || property.PetPolicy != PetPolicyNotAllowed {
		t.Fatalf("normalized property = %+v", property)
	}
	if !reflect.DeepEqual(property.FlexSpaces, []string{"office", ParkingPrivateGarage}) {
		t.Fatalf("FlexSpaces = %#v", property.FlexSpaces)
	}
}

func TestRentalHistoryRecencyUsesLatestCurrentRentalListing(t *testing.T) {
	t.Parallel()

	raw := map[string]any{
		"priceHistory": []any{
			map[string]any{"date": "2026-07-18", "event": "Price change", "postingIsRental": true},
			map[string]any{"date": "2026-07-10", "event": "Listed for rent", "postingIsRental": true},
			map[string]any{"date": "2025-04-01", "event": "Listing removed", "postingIsRental": true},
			map[string]any{"date": "2025-03-01", "event": "Listed for rent", "postingIsRental": true},
		},
	}
	listed, updated, days := rentalHistoryRecency(raw, time.Date(2026, time.July, 21, 0, 0, 0, 0, time.UTC))
	if listed != "2026-07-10" || updated != "2026-07-18" || days == nil || *days != 11 {
		t.Fatalf("recency = (%q, %q, %v)", listed, updated, days)
	}
}
