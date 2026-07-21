package cli

import (
	"reflect"
	"testing"

	"gozillo/internal/zillow"
)

func TestFilterListingsByLocationBoundaryUsesZIPCityAndAliases(t *testing.T) {
	t.Parallel()

	allowed, err := parseAllowedCities([]string{"Target City,Alias City"})
	if err != nil {
		t.Fatal(err)
	}
	aliases, err := parseLocationCityAliases([]string{"Neighborhood CA=Alias City"})
	if err != nil {
		t.Fatal(err)
	}
	options := locationBoundaryOptions{Strict: true, AllowedCities: allowed, CityAliases: aliases}

	zipListings := []zillow.Listing{
		{ID: "match", Address: zillow.Address{City: "Target City", PostalCode: "12345"}},
		{ID: "wrong-zip", Address: zillow.Address{City: "Target City", PostalCode: "12346"}},
		{ID: "wrong-city", Address: zillow.Address{City: "Outside City", PostalCode: "12345"}},
	}
	if got := listingIDs(filterListingsByLocationBoundary(zipListings, "12345", options, false)); !reflect.DeepEqual(got, []string{"match"}) {
		t.Fatalf("ZIP filtered IDs = %v", got)
	}

	cityListings := []zillow.Listing{
		{ID: "alias", Address: zillow.Address{City: "Alias City", State: "CA"}},
		{ID: "outside", Address: zillow.Address{City: "Target City", State: "CA"}},
	}
	if got := listingIDs(filterListingsByLocationBoundary(cityListings, "Neighborhood CA", options, false)); !reflect.DeepEqual(got, []string{"alias"}) {
		t.Fatalf("city filtered IDs = %v", got)
	}
}

func TestLocationBoundaryDefersUnknownSearchCardUntilAfterEnrichment(t *testing.T) {
	t.Parallel()

	options := locationBoundaryOptions{Strict: true, AllowedCities: map[string]struct{}{"target city": {}}}
	searchCard := zillow.Listing{ID: "pending"}
	if got := filterListingsByLocationBoundary([]zillow.Listing{searchCard}, "12345", options, true); len(got) != 1 {
		t.Fatalf("unknown search card was removed before enrichment: %+v", got)
	}
	searchCard.DetailStatus = zillow.DetailStatusUnavailable
	if got := filterListingsByLocationBoundary([]zillow.Listing{searchCard}, "12345", options, false); len(got) != 0 {
		t.Fatalf("unknown final result survived strict boundary: %+v", got)
	}
}
