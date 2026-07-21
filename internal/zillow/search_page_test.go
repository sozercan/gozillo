package zillow

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestFetchSearchPageParsesNextData(t *testing.T) {
	t.Parallel()

	nextData := map[string]any{
		"props": map[string]any{
			"pageProps": map[string]any{
				"searchPageState": map[string]any{
					"queryState": map[string]any{},
					"cat1": map[string]any{
						"searchResults": map[string]any{"listResults": []any{
							map[string]any{"zpid": "123", "address": "123 Coast Hwy", "unformattedPrice": 3200},
						}},
						"searchList": map[string]any{"totalResultCount": 1},
					},
				},
			},
		},
	}
	encoded, _ := json.Marshal(nextData)
	client, _ := newLocalZillowClient(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/html")
		_, _ = writer.Write([]byte(`<html><script id="__NEXT_DATA__" type="application/json">` + string(encoded) + `</script></html>`))
	}))
	result, err := client.FetchSearchPage(context.Background(), "https://www.zillow.com/94044/rentals/")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Listings) != 1 || result.Listings[0].ID != "123" || result.Metadata.TotalResults != 1 {
		t.Fatalf("result = %+v", result)
	}
}

func TestSearchResponseNormalizesBuildingCostsAndIdentity(t *testing.T) {
	t.Parallel()

	response := map[string]any{
		"cat1": map[string]any{
			"searchResults": map[string]any{
				"listResults": []any{
					map[string]any{
						"id":                                   "building-1",
						"detailUrl":                            "/apartments/example/ABC/",
						"isBuilding":                           true,
						"minBaseRent":                          4300,
						"totalRequiredMonthlyMinFee":           150,
						"listPriceIncludesRequiredMonthlyFees": false,
						"statusType":                           "FOR_RENT",
						"units": []any{
							map[string]any{"beds": "2", "price": "$4,300", "roomForRent": false},
							map[string]any{"beds": "3", "price": "$5,000", "roomForRent": false},
						},
					},
				},
			},
			"searchList": map[string]any{"totalResultCount": 1, "totalPages": 1},
		},
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	result, err := decodeSearchResponse(encoded, 1, map[string]any{"pagination": map[string]any{"currentPage": 1}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Listings) != 1 {
		t.Fatalf("listings = %d", len(result.Listings))
	}
	listing := result.Listings[0]
	if !listing.IsBuilding || listing.HomeType != "APARTMENT" || listing.Price == nil || *listing.Price != 4300 {
		t.Fatalf("building listing = %+v", listing)
	}
	if listing.RequiredMonthlyFees == nil || *listing.RequiredMonthlyFees != 150 || listing.TotalMonthlyCost == nil || *listing.TotalMonthlyCost != 4450 {
		t.Fatalf("building costs = %+v", listing)
	}
	if listing.Bedrooms == nil || *listing.Bedrooms != 3 {
		t.Fatalf("building bedrooms = %+v", listing.Bedrooms)
	}
}

func TestSearchResponseMergesListAndMapResults(t *testing.T) {
	t.Parallel()

	response := map[string]any{
		"cat1": map[string]any{
			"searchResults": map[string]any{
				"listResults": []any{
					map[string]any{"zpid": "1", "detailUrl": "/homedetails/1_zpid/", "unformattedPrice": 4000},
				},
				"mapResults": []any{
					map[string]any{"zpid": "1", "detailUrl": "/homedetails/1_zpid/", "unformattedPrice": 4000},
					map[string]any{"zpid": "2", "unformattedPrice": 4200},
				},
			},
			"searchList": map[string]any{"totalResultCount": 2, "totalPages": 1},
		},
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatal(err)
	}
	result, err := decodeSearchResponse(encoded, 1, map[string]any{"pagination": map[string]any{"currentPage": 1}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Listings) != 2 || result.Listings[0].ID != "1" || result.Listings[1].ID != "2" {
		t.Fatalf("merged listings = %+v", result.Listings)
	}
	if result.Listings[1].URL != "https://www.zillow.com/homedetails/2_zpid/" {
		t.Fatalf("map-only URL = %q", result.Listings[1].URL)
	}
}
