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
	if unit.Laundry != LaundryInUnit || unit.Availability != "2026-08-05" || unit.HomeType != "APARTMENT" || unit.Status != "" {
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

func TestReadRentalPageCommunityPrefersUnitAliasesOverFloorPlanFallbacks(t *testing.T) {
	t.Parallel()

	page := readCommunityRentalPage(t, map[string]any{
		"streetAddress": "100 Example Ave",
		"city":          "Example",
		"state":         "CA",
		"zipcode":       "90000",
		"description":   "Building description",
		"floorPlans": []any{
			map[string]any{
				"zpid":          "9100",
				"baseRent":      4400,
				"beds":          2,
				"baths":         1,
				"sqft":          900,
				"availableFrom": "2026-08-01",
				"description":   "Floor plan description",
				"homeStatus":    "OFF_MARKET",
				"units": []any{
					map[string]any{
						"zpid":                  "9200",
						"price":                 4600,
						"bedrooms":              3,
						"bathrooms":             2,
						"livingArea":            1100,
						"availabilityDate":      "2026-08-09",
						"additionalInformation": "Unit description",
						"statusType":            "COMING_SOON",
					},
				},
			},
		},
	})
	if len(page.Properties) != 1 {
		t.Fatalf("properties = %d, want 1", len(page.Properties))
	}

	property := page.Properties[0]
	if property.Price == nil || *property.Price != 4600 {
		t.Fatalf("price = %v, want unit price 4600", property.Price)
	}
	if property.Bedrooms == nil || *property.Bedrooms != 3 || property.Bathrooms == nil || *property.Bathrooms != 2 || property.LivingArea == nil || *property.LivingArea != 1100 {
		t.Fatalf("layout = %+v, want unit layout aliases", property)
	}
	if property.Availability != "2026-08-09" {
		t.Fatalf("availability = %q, want unit availabilityDate", property.Availability)
	}
	if property.Description != "Unit description | Floor plan description | Building description" {
		t.Fatalf("description = %q, want unit description first", property.Description)
	}
	if property.Status != "COMING_SOON" {
		t.Fatalf("status = %q, want unit status", property.Status)
	}
}

func TestReadRentalPageCommunityKeepsUnitsWithoutZPIDDistinct(t *testing.T) {
	t.Parallel()

	page := readCommunityRentalPage(t, map[string]any{
		"streetAddress": "100 Example Ave",
		"city":          "Example",
		"state":         "CA",
		"zipcode":       "90000",
		"floorPlans": []any{
			map[string]any{
				"zpid": "9100",
				"name": "Plan A",
				"units": []any{
					map[string]any{"id": "unit-a", "baseRent": 4500},
					map[string]any{"id": "unit-b", "baseRent": 4600},
				},
			},
		},
	})
	if len(page.Properties) != 2 {
		t.Fatalf("properties = %d, want 2 distinct units", len(page.Properties))
	}
	for index, property := range page.Properties {
		if property.ID != "" {
			t.Fatalf("property %d ID = %q, want no inherited or synthetic Zillow ID", index, property.ID)
		}
		if property.URL != "" {
			t.Fatalf("property %d URL = %q, want no fabricated Zillow URL", index, property.URL)
		}
	}
	if page.Properties[0].Price == nil || *page.Properties[0].Price != 4500 || page.Properties[1].Price == nil || *page.Properties[1].Price != 4600 {
		t.Fatalf("unit prices = %+v", page.Properties)
	}
}

func TestReadRentalPageCommunityKeepsFloorPlansWithoutZPIDDistinct(t *testing.T) {
	t.Parallel()

	page := readCommunityRentalPage(t, map[string]any{
		"streetAddress": "100 Example Ave",
		"city":          "Example",
		"state":         "CA",
		"zipcode":       "90000",
		"floorPlans": []any{
			map[string]any{"id": "plan-a", "name": "Plan A", "minPrice": 4000, "units": []any{}},
			map[string]any{"id": "plan-b", "name": "Plan B", "minPrice": 4100, "units": []any{}},
		},
	})
	if len(page.Properties) != 2 {
		t.Fatalf("properties = %d, want 2 distinct floor plans", len(page.Properties))
	}
	for index, property := range page.Properties {
		if property.ID != "" {
			t.Fatalf("property %d ID = %q, want no synthetic Zillow ID", index, property.ID)
		}
	}
	if page.Properties[0].Price == nil || *page.Properties[0].Price != 4000 || page.Properties[1].Price == nil || *page.Properties[1].Price != 4100 {
		t.Fatalf("floor plan prices = %+v", page.Properties)
	}
}

func TestReadRentalPageCommunityUsesFloorPlanStatusFallbackAndLeavesMissingStatusUnknown(t *testing.T) {
	t.Parallel()

	page := readCommunityRentalPage(t, map[string]any{
		"streetAddress": "100 Example Ave",
		"city":          "Example",
		"state":         "CA",
		"zipcode":       "90000",
		"floorPlans": []any{
			map[string]any{
				"zpid":       "9100",
				"statusType": "COMING_SOON",
				"units": []any{
					map[string]any{"zpid": "9200", "baseRent": 4500},
				},
			},
			map[string]any{
				"zpid": "9300",
				"units": []any{
					map[string]any{"zpid": "9400", "baseRent": 4600},
				},
			},
		},
	})
	if len(page.Properties) != 2 {
		t.Fatalf("properties = %d, want 2", len(page.Properties))
	}
	if page.Properties[0].Status != "COMING_SOON" {
		t.Fatalf("fallback status = %q, want floor-plan status", page.Properties[0].Status)
	}
	if page.Properties[1].Status != "" {
		t.Fatalf("missing status = %q, want unknown", page.Properties[1].Status)
	}
}

func readCommunityRentalPage(t *testing.T, building map[string]any) *RentalPage {
	t.Helper()

	nextData := map[string]any{
		"props": map[string]any{
			"pageProps": map[string]any{
				"componentProps": map[string]any{
					"initialReduxState": map[string]any{
						"gdp": map[string]any{"building": building},
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
	return page
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
