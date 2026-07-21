package zillow

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestReadSearchSnapshotParsesHTMLAndRawNextDataJSON(t *testing.T) {
	t.Parallel()

	state := currentSearchSnapshotState("12345", "123 CAPTCHA Way, Seattle, WA 98101")
	nextData := searchSnapshotNextData(state)
	htmlDocument := `<!doctype html><html><head>` +
		`<script nonce="synthetic" type="application/json" id="__NEXT_DATA__">` + nextData + `</script>` +
		`</head><body>ordinary search page</body></html>`

	tests := []struct {
		name  string
		input string
	}{
		{name: "saved HTML", input: htmlDocument},
		{name: "raw Next data JSON", input: "\ufeff\n  " + nextData + "  \n"},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			result, err := ReadSearchSnapshotWithOptions(strings.NewReader(test.input), SearchSnapshotOptions{IncludeRaw: true})
			if err != nil {
				t.Fatalf("ReadSearchSnapshotWithOptions() error = %v", err)
			}
			assertCurrentSearchSnapshotResult(t, result, "12345")

			var gotState, wantState any
			if err := json.Unmarshal(result.Raw, &gotState); err != nil {
				t.Fatalf("result.Raw is not valid JSON: %v", err)
			}
			if err := json.Unmarshal([]byte(state), &wantState); err != nil {
				t.Fatalf("test state is not valid JSON: %v", err)
			}
			if !jsonValuesEqual(gotState, wantState) {
				t.Fatalf("result.Raw = %s, want searchPageState %s", result.Raw, state)
			}
			if strings.Contains(string(result.Raw), `"props"`) {
				t.Fatalf("result.Raw retained the Next data envelope: %s", result.Raw)
			}
		})
	}
}

func TestReadSearchSnapshotDoesNotTreatListingTextAsChallenge(t *testing.T) {
	t.Parallel()

	state := currentSearchSnapshotState("challenge-words", "Verify you are human at 1 Robot Check CAPTCHA Lane")
	document := `<html><body><script id="__NEXT_DATA__" type="application/json">` +
		searchSnapshotNextData(state) +
		`</script><p>Press &amp; Hold is listing text.</p></body></html>`

	result, err := ReadSearchSnapshot(strings.NewReader(document))
	if err != nil {
		t.Fatalf("ReadSearchSnapshot() error = %v", err)
	}
	if len(result.Listings) != 1 || result.Listings[0].ID != "challenge-words" {
		t.Fatalf("Listings = %+v", result.Listings)
	}
	if len(result.Raw) != 0 {
		t.Fatalf("Raw = %s, want omitted by default", result.Raw)
	}
}

func TestReadSearchSnapshotRejectsMissingOrInvalidState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
	}{
		{name: "invalid Next data JSON", input: `{"props":`},
		{name: "missing props", input: `{}`},
		{name: "invalid props", input: `{"props":[]}`},
		{name: "missing page props", input: `{"props":{}}`},
		{name: "missing search state", input: `{"props":{"pageProps":{}}}`},
		{name: "null search state", input: `{"props":{"pageProps":{"searchPageState":null}}}`},
		{name: "non-object search state", input: `{"props":{"pageProps":{"searchPageState":"invalid"}}}`},
		{name: "invalid listing array", input: `{"props":{"pageProps":{"searchPageState":{"cat1":{"searchResults":{"listResults":{}}}}}}}`},
		{name: "invalid listing", input: `{"props":{"pageProps":{"searchPageState":{"cat1":{"searchResults":{"listResults":[{"beds":{}}]}}}}}}`},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := ReadSearchSnapshot(strings.NewReader(test.input))
			if !errors.Is(err, ErrSchemaDrift) {
				t.Fatalf("ReadSearchSnapshot() error = %v, want schema drift", err)
			}
			var drift *SchemaDriftError
			if !errors.As(err, &drift) {
				t.Fatalf("ReadSearchSnapshot() error type = %T, want *SchemaDriftError", err)
			}
		})
	}
}

func TestReadSearchSnapshotEnforcesSizeLimit(t *testing.T) {
	t.Parallel()

	_, err := ReadSearchSnapshotWithOptions(
		strings.NewReader(strings.Repeat("x", 33)),
		SearchSnapshotOptions{MaxBytes: 32},
	)
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("ReadSearchSnapshotWithOptions() error = %v, want response-too-large", err)
	}
	var tooLarge *ResponseTooLargeError
	if !errors.As(err, &tooLarge) {
		t.Fatalf("error type = %T, want *ResponseTooLargeError", err)
	}
	if tooLarge.Limit != 32 {
		t.Fatalf("ResponseTooLargeError.Limit = %d, want 32", tooLarge.Limit)
	}
}

func TestReadSearchSnapshotIgnoresFakeNextDataScripts(t *testing.T) {
	t.Parallel()

	fake := searchSnapshotNextData(currentSearchSnapshotState("fake", "999 Fake St"))
	real := searchSnapshotNextData(currentSearchSnapshotState("real", "100 Real St"))

	tests := []struct {
		name   string
		prefix string
	}{
		{
			name:   "HTML comment",
			prefix: `<!-- <script id="__NEXT_DATA__" type="application/json">` + fake + `</script> -->`,
		},
		{
			name: "other script body",
			prefix: `<script>` +
				`window.notNextData = ` + fake + `;` +
				`window.fakeTag = "<script id='__NEXT_DATA__'>";` +
				`</script>`,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			document := `<html><head>` + test.prefix +
				`<script id="__NEXT_DATA__" type="application/json">` + real + `</script>` +
				`</head><body></body></html>`

			result, err := ReadSearchSnapshot(strings.NewReader(document))
			if err != nil {
				t.Fatalf("ReadSearchSnapshot() error = %v", err)
			}
			if len(result.Listings) != 1 || result.Listings[0].ID != "real" {
				t.Fatalf("Listings = %+v, want real script listing", result.Listings)
			}
		})
	}

	t.Run("fake comment without real script", func(t *testing.T) {
		t.Parallel()
		document := `<html><body><!-- <script id="__NEXT_DATA__">` + fake + `</script> --></body></html>`
		_, err := ReadSearchSnapshot(strings.NewReader(document))
		if !errors.Is(err, ErrSchemaDrift) {
			t.Fatalf("ReadSearchSnapshot() error = %v, want schema drift", err)
		}
	})
}

func TestReadSearchSnapshotDetectsChallengeHTML(t *testing.T) {
	t.Parallel()

	document := `<!doctype html><html><head><title>Access denied</title></head><body>` +
		`<!-- <script id="__NEXT_DATA__">not real</script> -->` +
		`<div id="px-captcha">Press &amp; Hold to verify you are human</div>` +
		`</body></html>`

	_, err := ReadSearchSnapshot(strings.NewReader(document))
	if !errors.Is(err, ErrChallenge) {
		t.Fatalf("ReadSearchSnapshot() error = %v, want challenge", err)
	}
	var challenge *ChallengeError
	if !errors.As(err, &challenge) {
		t.Fatalf("error type = %T, want *ChallengeError", err)
	}
	if challenge.Reason == "" {
		t.Fatal("ChallengeError.Reason is empty")
	}
}

func currentSearchSnapshotState(id, address string) string {
	return fmt.Sprintf(`{
		"cat1": {
			"searchResults": {
				"listResults": [{
					"zpid": %q,
					"detailUrl": "/homedetails/snapshot/%s_zpid/",
					"address": %q,
					"unformattedPrice": 725000,
					"price": "$725,000",
					"beds": 3,
					"baths": 2.5,
					"area": 1840,
					"homeType": "SINGLE_FAMILY",
					"statusType": "FOR_SALE",
					"latLong": {"latitude": 47.61, "longitude": -122.33}
				}],
				"resultsHash": "snapshot-hash",
				"relaxedResults": true
			},
			"searchList": {"totalResultCount": 17}
		},
		"searchQueryState": {"pagination": {"currentPage": 3}}
	}`, id, id, address)
}

func searchSnapshotNextData(state string) string {
	return `{"buildId":"synthetic","props":{"pageProps":{"searchPageState":` + state + `}},"page":"/homes"}`
}

func assertCurrentSearchSnapshotResult(t *testing.T, result *SearchResult, wantID string) {
	t.Helper()
	if result == nil {
		t.Fatal("result is nil")
	}
	if len(result.Listings) != 1 {
		t.Fatalf("len(Listings) = %d, want 1", len(result.Listings))
	}
	listing := result.Listings[0]
	if listing.ID != wantID {
		t.Fatalf("listing.ID = %q, want %q", listing.ID, wantID)
	}
	if listing.URL != "https://www.zillow.com/homedetails/snapshot/"+wantID+"_zpid/" {
		t.Fatalf("listing.URL = %q", listing.URL)
	}
	if listing.Price == nil || *listing.Price != 725000 {
		t.Fatalf("listing.Price = %v, want 725000", listing.Price)
	}
	if listing.Bedrooms == nil || *listing.Bedrooms != 3 || listing.Bathrooms == nil || *listing.Bathrooms != 2.5 {
		t.Fatalf("listing beds/baths = (%v, %v)", listing.Bedrooms, listing.Bathrooms)
	}
	if listing.LivingArea == nil || *listing.LivingArea != 1840 {
		t.Fatalf("listing.LivingArea = %v, want 1840", listing.LivingArea)
	}
	if result.Metadata.CurrentPage != 3 || result.Metadata.Returned != 1 || result.Metadata.TotalResults != 17 {
		t.Fatalf("Metadata = %+v", result.Metadata)
	}
	if result.Metadata.ResultsHash != "snapshot-hash" || !result.Metadata.RelaxedResults {
		t.Fatalf("Metadata extras = %+v", result.Metadata)
	}
}

func jsonValuesEqual(left, right any) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && string(leftJSON) == string(rightJSON)
}
