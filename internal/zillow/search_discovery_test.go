package zillow

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"testing"
)

func TestDiscoverLocationPaginatesServerFilteredResults(t *testing.T) {
	t.Parallel()

	var mu sync.Mutex
	requestedPages := make([]int, 0, 2)
	client, _ := newLocalZillowClient(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/example-ca/rentals/":
			if request.Method != http.MethodGet {
				t.Errorf("bootstrap method = %s, want GET", request.Method)
			}
			writeSearchBootstrapHTML(t, writer)
		case searchEndpointPath:
			if request.Method != http.MethodPut {
				t.Errorf("search method = %s, want PUT", request.Method)
			}
			var payload searchPayload
			decoder := json.NewDecoder(request.Body)
			decoder.UseNumber()
			if err := decoder.Decode(&payload); err != nil {
				t.Fatalf("decode payload: %v", err)
			}
			page := searchPage(payload.SearchQueryState)
			mu.Lock()
			requestedPages = append(requestedPages, page)
			mu.Unlock()
			assertPathNumber(t, payload.SearchQueryState, 2, "filterState", "beds", "min")
			assertPathNumber(t, payload.SearchQueryState, 2, "filterState", "beds", "max")
			assertPathString(t, payload.SearchQueryState, "days", "filterState", "sortSelection", "value")
			assertPathBool(t, payload.SearchQueryState, true, "filterState", "onlyRentalInUnitLaundry", "value")
			assertPathBool(t, payload.SearchQueryState, true, "filterState", "isEntirePlaceForRent", "value")
			assertPathBool(t, payload.SearchQueryState, false, "filterState", "isRoomForRent", "value")
			assertPathBool(t, payload.SearchQueryState, true, "filterState", "isApartment", "value")
			assertPathBool(t, payload.SearchQueryState, false, "filterState", "isManufactured", "value")
			wants, ok := payload.Wants["cat1"].([]any)
			if !ok || fmt.Sprint(wants) != "[listResults mapResults]" {
				t.Errorf("wants.cat1 = %#v", payload.Wants["cat1"])
			}

			listings := []map[string]any{
				{"zpid": fmt.Sprintf("%d01", page), "detailUrl": fmt.Sprintf("/homedetails/%d01_zpid/", page), "unformattedPrice": 3000},
				{"zpid": "shared", "detailUrl": "/homedetails/shared/", "unformattedPrice": 3100},
			}
			if page == 2 {
				listings = append(listings, map[string]any{"zpid": "202", "detailUrl": "/homedetails/202_zpid/", "unformattedPrice": 3200})
			}
			writeSearchDiscoveryJSON(t, writer, listings, 2, 3)
		default:
			http.NotFound(writer, request)
		}
	}))

	result, err := client.DiscoverLocation(context.Background(), "https://www.zillow.com/example-ca/rentals/", DiscoveryOptions{
		MaxPages: 5,
		Routes: []SearchRoute{{
			Name: "exact-2-newest",
			Filters: SearchFilters{
				MinBeds:         2,
				MaxBeds:         2,
				Sort:            "days",
				HomeTypes:       []string{HomeTypeApartment, HomeTypeCondo, HomeTypeTownhouse, HomeTypeSingleFamily},
				InUnitLaundry:   true,
				EntirePlaceOnly: true,
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Listings) != 4 {
		t.Fatalf("listings = %d, want 4 deduplicated results", len(result.Listings))
	}
	if len(result.Issues) != 0 {
		t.Fatalf("issues = %+v, want none", result.Issues)
	}
	if len(result.Coverage) != 1 || result.Coverage[0].PagesFetched != 2 || !result.Coverage[0].Complete {
		t.Fatalf("coverage = %+v", result.Coverage)
	}
	mu.Lock()
	defer mu.Unlock()
	if fmt.Sprint(requestedPages) != "[1 2]" {
		t.Fatalf("requested pages = %v, want [1 2]", requestedPages)
	}
}

func writeSearchBootstrapHTML(t *testing.T, writer http.ResponseWriter) {
	t.Helper()
	nextData := map[string]any{
		"props": map[string]any{
			"pageProps": map[string]any{
				"searchPageState": map[string]any{
					"queryState": map[string]any{
						"filterState":     map[string]any{"isForRent": map[string]any{"value": true}},
						"regionSelection": []any{map[string]any{"regionId": 123, "regionType": 6}},
					},
					"cat1": map[string]any{
						"searchResults": map[string]any{"listResults": []any{}},
						"searchList":    map[string]any{"totalResultCount": 0},
					},
				},
			},
		},
	}
	encoded, err := json.Marshal(nextData)
	if err != nil {
		t.Fatal(err)
	}
	writer.Header().Set("Content-Type", "text/html")
	_, _ = fmt.Fprintf(writer, `<html><script id="__NEXT_DATA__" type="application/json">%s</script></html>`, encoded)
}

func writeSearchDiscoveryJSON(t *testing.T, writer http.ResponseWriter, listings []map[string]any, totalPages, totalResults int) {
	t.Helper()
	payload := map[string]any{
		"cat1": map[string]any{
			"searchResults": map[string]any{"listResults": listings},
			"searchList": map[string]any{
				"totalPages":       totalPages,
				"resultsPerPage":   41,
				"totalResultCount": totalResults,
			},
		},
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	writeJSON(t, writer, http.StatusOK, string(encoded))
}

func assertPathBool(t *testing.T, root map[string]any, want bool, path ...string) {
	t.Helper()
	value, ok := lookupPath(root, path...)
	if !ok {
		t.Fatalf("path %v missing", path)
	}
	got, ok := boolFromAny(value)
	if !ok || got != want {
		t.Fatalf("path %v = %#v, want %t", path, value, want)
	}
}

func TestDiscoverLocationDoesNotStopASecondRouteOnCrossRouteDuplicates(t *testing.T) {
	t.Parallel()

	client, _ := newLocalZillowClient(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/example-ca/rentals/" {
			writeSearchBootstrapHTML(t, writer)
			return
		}
		if request.URL.Path != searchEndpointPath {
			http.NotFound(writer, request)
			return
		}
		var payload searchPayload
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		page := searchPage(payload.SearchQueryState)
		sortValue, _ := lookupPath(payload.SearchQueryState, "filterState", "sortSelection", "value")
		id := "shared"
		if sortValue == "mostrecentchange" && page == 2 {
			id = "updated-only"
		}
		writeSearchDiscoveryJSON(t, writer, []map[string]any{{"zpid": id, "detailUrl": "/homedetails/" + id + "/"}}, 2, 2)
	}))

	result, err := client.DiscoverLocation(context.Background(), "https://www.zillow.com/example-ca/rentals/", DiscoveryOptions{
		MaxPages: 2,
		Routes: []SearchRoute{
			{Name: "newest", Filters: SearchFilters{Sort: "days"}},
			{Name: "updated", Filters: SearchFilters{Sort: "mostrecentchange"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Listings) != 2 || result.Listings[1].ID != "updated-only" {
		t.Fatalf("listings = %+v", result.Listings)
	}
	if len(result.Coverage) != 2 || result.Coverage[1].PagesFetched != 2 {
		t.Fatalf("coverage = %+v", result.Coverage)
	}
}
