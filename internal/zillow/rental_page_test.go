package zillow

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestReadRentalPageExpandsAvailableCommunityUnits(t *testing.T) {
	t.Parallel()

	nextData := map[string]any{
		"props": map[string]any{
			"pageProps": map[string]any{
				"componentProps": map[string]any{
					"initialReduxState": map[string]any{
						"gdp": map[string]any{
							"building": map[string]any{
								"zpid":          "9000",
								"streetAddress": "100 Example Ave",
								"city":          "Example",
								"state":         "CA",
								"zipcode":       "90000",
								"buildingName":  "Example Apartments",
								"description":   "Apartment community",
								"buildingAttributes": map[string]any{
									"hasSharedLaundry": true,
								},
								"floorPlans": []any{
									map[string]any{
										"zpid":          "9100",
										"name":          "Plan A",
										"beds":          2,
										"baths":         2,
										"sqft":          1050,
										"minBaseRent":   4400,
										"availableFrom": "2026-08-01",
										"amenityDetails": []any{
											"In-unit washer and dryer",
											"Private garage",
										},
										"units": []any{
											map[string]any{
												"zpid":                                 "9200",
												"unitNumber":                           "A",
												"baseRent":                             4500,
												"totalRequiredMonthlyMinFee":           175,
												"listPriceIncludesRequiredMonthlyFees": false,
												"availableFrom":                        "2026-08-05",
											},
										},
									},
									map[string]any{
										"zpid":           "9300",
										"name":           "Plan B",
										"beds":           3,
										"baths":          2,
										"sqft":           1250,
										"minPrice":       5200,
										"availableFrom":  "2026-07-25",
										"amenityDetails": []any{"Washer", "Dryer"},
										"units":          []any{},
									},
								},
							},
						},
					},
				},
			},
		},
	}
	encoded, err := json.Marshal(nextData)
	if err != nil {
		t.Fatal(err)
	}
	html := fmt.Sprintf(`<html><script id="__NEXT_DATA__" type="application/json">%s</script></html>`, encoded)

	page, err := ReadRentalPage(strings.NewReader(html))
	if err != nil {
		t.Fatal(err)
	}
	if page.Kind != RentalPageCommunity {
		t.Fatalf("kind = %q, want %q", page.Kind, RentalPageCommunity)
	}
	if len(page.Properties) != 2 {
		t.Fatalf("properties = %d, want 2", len(page.Properties))
	}

	unit := page.Properties[0]
	if unit.ID != "9200" || unit.Price == nil || *unit.Price != 4500 {
		t.Fatalf("unit identity/price = %+v", unit)
	}
	if unit.RequiredMonthlyFees == nil || *unit.RequiredMonthlyFees != 175 || unit.TotalMonthlyCost == nil || *unit.TotalMonthlyCost != 4675 {
		t.Fatalf("unit fees = %+v", unit)
	}
	if unit.Bedrooms == nil || *unit.Bedrooms != 2 || unit.Bathrooms == nil || *unit.Bathrooms != 2 || unit.LivingArea == nil || *unit.LivingArea != 1050 {
		t.Fatalf("unit layout = %+v", unit)
	}
	if unit.Laundry != LaundryInUnit || unit.Availability != "2026-08-05" || unit.HomeType != "APARTMENT" || unit.Status != "FOR_RENT" {
		t.Fatalf("unit facts = %+v", unit)
	}
	if !containsString(unit.FlexSpaces, ParkingPrivateGarage) || !containsSubstring(unit.VerificationNotes, "garage") {
		t.Fatalf("unit verification = %+v", unit)
	}

	floorPlan := page.Properties[1]
	if floorPlan.ID != "9300" || floorPlan.Price == nil || *floorPlan.Price != 5200 || floorPlan.Bedrooms == nil || *floorPlan.Bedrooms != 3 {
		t.Fatalf("floor plan = %+v", floorPlan)
	}
	if floorPlan.Laundry != LaundryInUnit || !containsSubstring(floorPlan.VerificationNotes, "floor plan") {
		t.Fatalf("floor plan verification = %+v", floorPlan)
	}
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func containsSubstring(values []string, wanted string) bool {
	for _, value := range values {
		if strings.Contains(strings.ToLower(value), strings.ToLower(wanted)) {
			return true
		}
	}
	return false
}
