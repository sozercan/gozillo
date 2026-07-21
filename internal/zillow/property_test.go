package zillow

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func TestReadPropertyExtractsNextDataCacheAndNormalizes(t *testing.T) {
	t.Parallel()

	document := propertyDocumentWithComponentProps(t, true, map[string]any{
		"query-a": map[string]any{
			"property": map[string]any{"price": 1},
		},
		"query-b": map[string]any{
			"property": map[string]any{
				"zpid":             987654,
				"id":               "UHJvcGVydHk6OTg3NjU0",
				"url":              "/homedetails/example/987654_zpid/",
				"streetAddress":    "42 Pine St",
				"city":             "Seattle",
				"state":            "WA",
				"zipcode":          "98101",
				"price":            "$765K",
				"unformattedPrice": 765432,
				"bedrooms":         3,
				"bathrooms":        2.5,
				"livingArea":       1750,
				"lotSize":          5000,
				"yearBuilt":        1998,
				"homeType":         "SINGLE_FAMILY",
				"homeStatus":       "FOR_SALE",
				"description":      "Test fixture only",
				"imgSrc":           "https://photos.example/property.jpg",
				"latitude":         47.6,
				"longitude":        -122.3,
			},
		},
	}, map[string]any{"zpid": 987654})

	property, err := ReadPropertyWithOptions(strings.NewReader(document), PropertyReaderOptions{IncludeRaw: true})
	if err != nil {
		t.Fatalf("ReadPropertyWithOptions() error = %v", err)
	}
	if property.ID != "987654" || property.URL != "https://www.zillow.com/homedetails/example/987654_zpid/" {
		t.Fatalf("property identity = (%q, %q)", property.ID, property.URL)
	}
	if property.Address.Full != "42 Pine St, Seattle WA 98101" {
		t.Fatalf("property address = %+v", property.Address)
	}
	if property.Price == nil || *property.Price != 765432 {
		t.Fatalf("property Price = %v, want exact unformatted value", property.Price)
	}
	if property.Bedrooms == nil || *property.Bedrooms != 3 || property.Bathrooms == nil || *property.Bathrooms != 2.5 {
		t.Fatalf("property beds/baths = (%v, %v)", property.Bedrooms, property.Bathrooms)
	}
	if property.LivingArea == nil || *property.LivingArea != 1750 || property.YearBuilt == nil || *property.YearBuilt != 1998 {
		t.Fatalf("property area/year = (%v, %v)", property.LivingArea, property.YearBuilt)
	}
	if property.Coordinates.Longitude == nil || *property.Coordinates.Longitude != -122.3 {
		t.Fatalf("property longitude = %v", property.Coordinates.Longitude)
	}
	if len(property.Raw) == 0 {
		t.Fatal("property Raw is empty")
	}
	var raw map[string]any
	if err := json.Unmarshal(property.Raw, &raw); err != nil {
		t.Fatalf("property Raw is not JSON: %v", err)
	}
	if raw["zpid"].(float64) != 987654 {
		t.Fatalf("raw zpid = %#v", raw["zpid"])
	}
}

func TestReadPropertyPrefersNormalizedZPIDOverGraphQLGlobalID(t *testing.T) {
	t.Parallel()

	document := propertyDocument(t, true, map[string]any{
		"query": map[string]any{
			"property": map[string]any{
				"id":     "UHJvcGVydHk6MTIz",
				"hdpUrl": "/homedetails/example/123_zpid/",
				"price":  450000,
			},
		},
	})

	property, err := ReadProperty(strings.NewReader(document))
	if err != nil {
		t.Fatalf("ReadProperty() error = %v", err)
	}
	if property.ID != "123" {
		t.Fatalf("property ID = %q, want normalized ZPID 123", property.ID)
	}
}

func TestReadPropertySelectsAuthoritativePageZPIDOverRicherComparables(t *testing.T) {
	t.Parallel()

	document := propertyDocumentWithComponentProps(t, true, map[string]any{
		"page-query": map[string]any{
			"property": map[string]any{
				"zpid":  123,
				"price": 425000,
			},
		},
		"comparable-a": map[string]any{
			"property": map[string]any{
				"zpid":          999,
				"url":           "/homedetails/comparable-a/999_zpid/",
				"streetAddress": "99 Richer Way",
				"city":          "Seattle",
				"state":         "WA",
				"zipcode":       "98101",
				"price":         999000,
				"bedrooms":      5,
				"bathrooms":     4,
				"livingArea":    4000,
				"homeType":      "SINGLE_FAMILY",
				"latitude":      47.61,
				"longitude":     -122.31,
			},
		},
		"comparable-b": map[string]any{
			"property": map[string]any{
				"zpid":          1000,
				"url":           "/homedetails/comparable-b/1000_zpid/",
				"streetAddress": "100 Richer Way",
				"city":          "Seattle",
				"state":         "WA",
				"zipcode":       "98102",
				"price":         1200000,
				"bedrooms":      6,
				"bathrooms":     5,
				"livingArea":    5000,
				"homeType":      "SINGLE_FAMILY",
				"latitude":      47.62,
				"longitude":     -122.32,
			},
		},
	}, map[string]any{"zpid": 123})

	property, err := ReadPropertyWithOptions(strings.NewReader(document), PropertyReaderOptions{IncludeRaw: true})
	if err != nil {
		t.Fatalf("ReadPropertyWithOptions() error = %v", err)
	}
	if property.ID != "123" {
		t.Fatalf("property ID = %q, want 123", property.ID)
	}
	if property.Price == nil || *property.Price != 425000 {
		t.Fatalf("property Price = %v, want 425000", property.Price)
	}
	if strings.Contains(string(property.Raw), "Richer Way") {
		t.Fatalf("property Raw contains a comparable: %s", property.Raw)
	}
}

func TestReadPropertyRejectsAmbiguousDistinctCandidates(t *testing.T) {
	t.Parallel()

	tests := map[string]map[string]any{
		"different zpids": {
			"query-a": map[string]any{
				"property": map[string]any{"zpid": 101, "price": 400000},
			},
			"query-b": map[string]any{
				"property": map[string]any{"zpid": 202, "price": 500000},
			},
		},
		"identity-less candidate and comparable": {
			"page-query": map[string]any{
				"property": map[string]any{"price": 400000, "streetAddress": "1 Unknown Home"},
			},
			"comparable": map[string]any{
				"property": map[string]any{"zpid": 202, "price": 500000},
			},
		},
	}
	for name, cache := range tests {
		name, cache := name, cache
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			document := propertyDocument(t, true, cache)
			_, err := ReadProperty(strings.NewReader(document))
			if !errors.Is(err, ErrSchemaDrift) {
				t.Fatalf("ReadProperty() error = %v, want schema drift", err)
			}
		})
	}
}

func TestReadPropertyRejectsInvalidPropertyValuesAndMissingIdentity(t *testing.T) {
	t.Parallel()

	tests := map[string]map[string]any{
		"invalid zpid type": {
			"zpid":  map[string]any{},
			"price": 450000,
		},
		"invalid price type": {
			"zpid":  123,
			"price": map[string]any{},
		},
		"invalid bedrooms type": {
			"zpid":     123,
			"bedrooms": []any{3},
		},
		"missing core identity": {
			"price": 450000,
		},
	}
	for name, propertyObject := range tests {
		name, propertyObject := name, propertyObject
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			document := propertyDocument(t, true, map[string]any{
				"query": map[string]any{"property": propertyObject},
			})
			_, err := ReadProperty(strings.NewReader(document))
			if !errors.Is(err, ErrSchemaDrift) {
				t.Fatalf("ReadProperty() error = %v, want schema drift", err)
			}
		})
	}
}

func TestExtractNextDataSkipsCommentsAndEarlierScriptBodies(t *testing.T) {
	t.Parallel()

	fake := `{"props":{"pageProps":{"componentProps":{"zpid":"111","gdpClientCache":"{}"}}}}`
	real := `{"props":{"pageProps":{"componentProps":{"zpid":"222","gdpClientCache":"{}"}}}}`
	document := `<!-- <script id="__NEXT_DATA__" type="application/json">` + fake + `</script> -->` +
		`<script>const disabled = '<script id="__NEXT_DATA__">` + fake + `</script>';</script>` +
		`<div data-markup="<script id='__NEXT_DATA__'>ignored</script>"></div>` +
		`<script id="__NEXT_DATA__" type="application/json">` + real + `</script>`
	data, ok := extractNextData([]byte(document))
	if !ok {
		t.Fatal("extractNextData() did not find real script")
	}
	if string(data) != real {
		t.Fatalf("extractNextData() = %s, want real script %s", data, real)
	}
}

func TestReadPropertyAcceptsObjectCache(t *testing.T) {
	t.Parallel()

	document := propertyDocument(t, false, map[string]any{
		"query": map[string]any{
			"property": map[string]any{
				"zpid": 1,
				"address": map[string]any{
					"streetAddress": "1 Oak Ave",
					"city":          "Portland",
					"state":         "OR",
					"zipcode":       "97201",
				},
			},
		},
	})
	property, err := ReadProperty(strings.NewReader(document))
	if err != nil {
		t.Fatalf("ReadProperty() error = %v", err)
	}
	if property.Address.Full != "1 Oak Ave, Portland OR 97201" {
		t.Fatalf("property address = %+v", property.Address)
	}
	if len(property.Raw) != 0 {
		t.Fatalf("property Raw = %q, want omitted", property.Raw)
	}
}

func TestFetchPropertyUsesSafeClientAndFallsBackToRequestedURL(t *testing.T) {
	t.Parallel()

	document := propertyDocument(t, true, map[string]any{
		"query": map[string]any{
			"property": map[string]any{
				"zpid":     123,
				"price":    "$450K",
				"bedrooms": 2,
			},
		},
		"richer-comparable": map[string]any{
			"property": map[string]any{
				"zpid":          999,
				"url":           "/homedetails/comparable/999_zpid/",
				"streetAddress": "99 Wrong Home",
				"city":          "Seattle",
				"state":         "WA",
				"zipcode":       "98101",
				"price":         999000,
				"bedrooms":      6,
				"bathrooms":     5,
				"livingArea":    5000,
				"homeType":      "SINGLE_FAMILY",
				"latitude":      47.6,
				"longitude":     -122.3,
			},
		},
	})
	var calls atomic.Int32
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if request.Method != http.MethodGet {
			t.Errorf("method = %q, want GET", request.Method)
		}
		if request.URL.Path != "/homedetails/example/123_zpid/" {
			t.Errorf("path = %q", request.URL.Path)
		}
		if request.UserAgent() != "gozillo/test-version" {
			t.Errorf("User-Agent = %q", request.UserAgent())
		}
		if accept := request.Header.Get("Accept"); !strings.Contains(accept, "text/html") {
			t.Errorf("Accept = %q", accept)
		}
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(writer, document)
	})
	client, _ := newLocalZillowClient(t, handler)
	property, err := client.FetchPropertyWithOptions(
		context.Background(),
		"https://www.zillow.com/homedetails/example/123_zpid/#ignored",
		PropertyOptions{IncludeRaw: true},
	)
	if err != nil {
		t.Fatalf("FetchPropertyWithOptions() error = %v", err)
	}
	if property.URL != "https://www.zillow.com/homedetails/example/123_zpid/" {
		t.Fatalf("property URL = %q", property.URL)
	}
	if property.Price == nil || *property.Price != 450000 {
		t.Fatalf("property Price = %v, want 450000", property.Price)
	}
	if calls.Load() != 1 {
		t.Fatalf("request count = %d, want 1", calls.Load())
	}
}

func TestPropertyIngestionClassifiesChallengeDriftAndSize(t *testing.T) {
	t.Parallel()

	t.Run("reader challenge", func(t *testing.T) {
		t.Parallel()
		_, err := ReadProperty(strings.NewReader(`<!doctype html><html><body><div id="px-captcha">Press & Hold</div></body></html>`))
		if !errors.Is(err, ErrChallenge) {
			t.Fatalf("ReadProperty() error = %v, want challenge", err)
		}
	})

	t.Run("missing next data", func(t *testing.T) {
		t.Parallel()
		_, err := ReadProperty(strings.NewReader(`<html><body>ordinary page</body></html>`))
		if !errors.Is(err, ErrSchemaDrift) {
			t.Fatalf("ReadProperty() error = %v, want schema drift", err)
		}
	})

	t.Run("invalid cache", func(t *testing.T) {
		t.Parallel()
		next := `{"props":{"pageProps":{"componentProps":{"gdpClientCache":"not-json"}}}}`
		document := `<script id="__NEXT_DATA__" type="application/json">` + next + `</script>`
		_, err := ReadProperty(strings.NewReader(document))
		if !errors.Is(err, ErrSchemaDrift) {
			t.Fatalf("ReadProperty() error = %v, want schema drift", err)
		}
	})

	t.Run("reader too large", func(t *testing.T) {
		t.Parallel()
		_, err := ReadPropertyWithOptions(strings.NewReader(strings.Repeat("x", 17)), PropertyReaderOptions{MaxBytes: 16})
		if !errors.Is(err, ErrResponseTooLarge) {
			t.Fatalf("ReadPropertyWithOptions() error = %v, want response-too-large", err)
		}
	})

	t.Run("direct x-px block", func(t *testing.T) {
		t.Parallel()
		var calls atomic.Int32
		client, _ := newLocalZillowClient(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			calls.Add(1)
			writer.Header().Set("X-Px-Blocked", "1")
			writer.Header().Set("Content-Type", "text/html")
			writer.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(writer, `<html>blocked</html>`)
		}))
		_, err := client.FetchProperty(context.Background(), "https://www.zillow.com/homedetails/1_zpid/")
		if !errors.Is(err, ErrChallenge) {
			t.Fatalf("FetchProperty() error = %v, want challenge", err)
		}
		if calls.Load() != 1 {
			t.Fatalf("request count = %d, want exactly 1", calls.Load())
		}
	})
}

func propertyDocument(t *testing.T, cacheAsString bool, cache any) string {
	t.Helper()
	return propertyDocumentWithComponentProps(t, cacheAsString, cache, nil)
}

func propertyDocumentWithComponentProps(t *testing.T, cacheAsString bool, cache any, extra map[string]any) string {
	t.Helper()
	cacheValue := cache
	if cacheAsString {
		encoded, err := json.Marshal(cache)
		if err != nil {
			t.Fatalf("json.Marshal(cache) error = %v", err)
		}
		cacheValue = string(encoded)
	}
	componentProps := map[string]any{"gdpClientCache": cacheValue}
	for key, value := range extra {
		componentProps[key] = value
	}
	nextData := map[string]any{
		"props": map[string]any{
			"pageProps": map[string]any{
				"componentProps": componentProps,
			},
		},
	}
	encoded, err := json.Marshal(nextData)
	if err != nil {
		t.Fatalf("json.Marshal(nextData) error = %v", err)
	}
	return `<!doctype html><html><head>` +
		`<script nonce="ignored">window.fixture=true</script>` +
		`<script type="application/json" nonce="abc>def" id='__NEXT_DATA__'>` + string(encoded) + `</script>` +
		`</head><body></body></html>`
}
