package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"gozillo/internal/output"
	"gozillo/internal/zillow"
)

func TestMultiLocationJSONLPreservesSuccessfulListingsAndErrors(t *testing.T) {
	t.Parallel()

	result := multiLocationSearchResult{Results: []locationSearchResult{
		{Location: "94501", Listings: []zillow.Listing{{ID: "123"}}},
		{Location: "94703", Error: "challenge response"},
	}}
	var buffer bytes.Buffer
	printer, err := output.NewPrinter(&buffer, output.ModeJSONL)
	if err != nil {
		t.Fatal(err)
	}
	if err := printMultiLocationResult(printer, output.ModeJSONL, result); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(buffer.String()), "\n")
	if len(lines) != 2 {
		t.Fatalf("lines = %q", lines)
	}
	var listingRecord locatedListing
	if err := json.Unmarshal([]byte(lines[0]), &listingRecord); err != nil {
		t.Fatal(err)
	}
	if listingRecord.Location != "94501" || listingRecord.Listing == nil || listingRecord.Listing.ID != "123" {
		t.Fatalf("listing record = %+v", listingRecord)
	}
	var errorRecord locatedListing
	if err := json.Unmarshal([]byte(lines[1]), &errorRecord); err != nil {
		t.Fatal(err)
	}
	if errorRecord.Location != "94703" || errorRecord.Error != "challenge response" || errorRecord.Listing != nil {
		t.Fatalf("error record = %+v", errorRecord)
	}
}

func TestMultiLocationTableIncludesErrorRows(t *testing.T) {
	t.Parallel()

	table := multiLocationTable(multiLocationSearchResult{Results: []locationSearchResult{
		{Location: "94501", Listings: []zillow.Listing{{ID: "123"}}},
		{Location: "94703", Error: "challenge response"},
	}})
	if len(table.Headers) < 2 || table.Headers[0] != "AREA" || table.Headers[1] != "ERROR" {
		t.Fatalf("headers = %#v", table.Headers)
	}
	if len(table.Rows) != 2 || table.Rows[1][0] != "94703" || table.Rows[1][1] != "challenge response" {
		t.Fatalf("rows = %#v", table.Rows)
	}
}
