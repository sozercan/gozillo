package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"gozillo/internal/zillow"
)

func TestVersionCommand(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Execute([]string{"version"}, &stdout, &stderr)
	if code != ExitOK {
		t.Fatalf("Execute(version) code = %d, stderr = %q", code, stderr.String())
	}
	if stdout.String() != "gozillo "+Version+"\n" {
		t.Fatalf("version output = %q", stdout.String())
	}
}

func TestHARCandidateDeriveAndSanitizeCommands(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("owner-only HAR/profile file writes are unsupported on Windows")
	}

	directory := t.TempDir()
	input := filepath.Join(directory, "capture.har")
	profilePath := filepath.Join(directory, "profile.json")
	sanitizedPath := filepath.Join(directory, "sanitized.har")
	if err := os.WriteFile(input, []byte(testHARDocument), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(profilePath, []byte("old permissive profile"), 0o644); err != nil {
		t.Fatal(err)
	}

	var candidatesOut bytes.Buffer
	var candidatesErr bytes.Buffer
	if code := Execute([]string{"har", "candidates", input}, &candidatesOut, &candidatesErr); code != ExitOK {
		t.Fatalf("har candidates code = %d, stderr = %q", code, candidatesErr.String())
	}
	if !strings.Contains(candidatesOut.String(), "/async-create-search-page-state") {
		t.Fatalf("candidate output missing endpoint: %q", candidatesOut.String())
	}

	var deriveOut bytes.Buffer
	var deriveErr bytes.Buffer
	if code := Execute([]string{"har", "derive", "--out", profilePath, input}, &deriveOut, &deriveErr); code != ExitOK {
		t.Fatalf("har derive code = %d, stderr = %q", code, deriveErr.String())
	}
	profile, err := zillow.LoadSearchProfileFile(profilePath)
	if err != nil {
		t.Fatalf("derived profile failed to load: %v", err)
	}
	if profile.Endpoint != "https://www.zillow.com/async-create-search-page-state" {
		t.Fatalf("derived endpoint = %q", profile.Endpoint)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(profilePath)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("derived profile mode = %#o, want 0600", got)
		}
	}

	var sanitizeOut bytes.Buffer
	var sanitizeErr bytes.Buffer
	if code := Execute([]string{"har", "sanitize", "--out", sanitizedPath, input}, &sanitizeOut, &sanitizeErr); code != ExitOK {
		t.Fatalf("har sanitize code = %d, stderr = %q", code, sanitizeErr.String())
	}
	data, err := os.ReadFile(sanitizedPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(bytes.ToLower(data), []byte(`"cookie"`)) || bytes.Contains(data, []byte("secret-session")) {
		t.Fatalf("sanitized HAR retained cookie data: %s", data)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(sanitizedPath)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("sanitized HAR mode = %#o, want 0600", got)
		}
	}
}

func TestPropertyCommandParsesLocalNextData(t *testing.T) {
	t.Parallel()

	cache := map[string]any{
		`ViewQuery{"zpid":"123"}`: map[string]any{
			"property": map[string]any{
				"zpid":          "123",
				"hdpUrl":        "/homedetails/example/123_zpid/",
				"streetAddress": "1 Main St",
				"city":          "Seattle",
				"state":         "WA",
				"zipcode":       "98101",
				"price":         750000,
				"bedrooms":      3,
				"bathrooms":     2.5,
				"livingArea":    1800,
				"yearBuilt":     1999,
			},
		},
	}
	cacheJSON, _ := json.Marshal(cache)
	nextData := map[string]any{
		"props": map[string]any{
			"pageProps": map[string]any{
				"componentProps": map[string]any{"gdpClientCache": string(cacheJSON)},
			},
		},
	}
	nextJSON, _ := json.Marshal(nextData)
	path := filepath.Join(t.TempDir(), "property.html")
	html := `<script id="__NEXT_DATA__" type="application/json">` + string(nextJSON) + `</script>`
	if err := os.WriteFile(path, []byte(html), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := Execute([]string{"--output=json", "property", path}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("property code = %d, stderr = %q", code, stderr.String())
	}
	var property zillow.Property
	if err := json.Unmarshal(stdout.Bytes(), &property); err != nil {
		t.Fatalf("decode property output: %v", err)
	}
	if property.ID != "123" || property.Price == nil || *property.Price != 750000 {
		t.Fatalf("property = %+v", property)
	}
}

const testHARDocument = `{
  "log": {
    "version": "1.2",
    "creator": {"name": "test", "version": "1"},
    "entries": [{
      "startedDateTime": "2026-07-19T16:45:38Z",
      "time": 1,
      "request": {
        "method": "PUT",
        "url": "https://www.zillow.com/async-create-search-page-state",
        "httpVersion": "HTTP/2",
        "headers": [
          {"name": "Content-Type", "value": "application/json"},
          {"name": "Referer", "value": "https://www.zillow.com/homes/for_sale/Seattle-WA/"},
          {"name": "Cookie", "value": "zgsession=secret-session"}
        ],
        "queryString": [],
        "cookies": [{"name": "zgsession", "value": "secret-session"}],
        "headersSize": -1,
        "bodySize": -1,
        "postData": {
          "mimeType": "application/json",
          "text": "{\"searchQueryState\":{\"regionSelection\":[{\"regionId\":16037,\"regionType\":6}]},\"wants\":{\"cat1\":[\"listResults\"]},\"requestId\":2}"
        }
      },
      "response": {
        "status": 200,
        "statusText": "OK",
        "httpVersion": "HTTP/2",
        "headers": [{"name": "Content-Type", "value": "application/json"}],
        "cookies": [],
        "content": {"size": 2, "mimeType": "application/json", "text": "{}"},
        "redirectURL": "",
        "headersSize": -1,
        "bodySize": -1
      },
      "cache": {},
      "timings": {"send": 0, "wait": 1, "receive": 0}
    }]
  }
}`

func TestSearchCommandReadsBrowserSnapshotAndFiltersLocally(t *testing.T) {
	t.Parallel()

	nextData := map[string]any{
		"props": map[string]any{
			"pageProps": map[string]any{
				"searchPageState": map[string]any{
					"queryState": map[string]any{"pagination": map[string]any{"currentPage": 1}},
					"cat1": map[string]any{
						"searchResults": map[string]any{
							"listResults": []any{
								map[string]any{"zpid": "1", "address": "1 Main St", "unformattedPrice": 450000, "beds": 2, "baths": 1},
								map[string]any{"zpid": "2", "address": "2 Pine St", "unformattedPrice": 750000, "beds": 3, "baths": 2},
							},
						},
						"searchList": map[string]any{"totalResultCount": 2},
					},
				},
			},
		},
	}
	data, err := json.Marshal(nextData)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "search.next.json")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Execute([]string{"--output=json", "search", "--snapshot", path, "--min-price", "600000", "--min-beds", "3"}, &stdout, &stderr)
	if code != ExitOK {
		t.Fatalf("search snapshot code = %d, stderr = %q", code, stderr.String())
	}
	var result zillow.SearchResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		t.Fatalf("decode search output: %v", err)
	}
	if len(result.Listings) != 1 || result.Listings[0].ID != "2" {
		t.Fatalf("filtered listings = %+v", result.Listings)
	}
	if result.Metadata.Returned != 1 || result.Metadata.TotalResults != 2 {
		t.Fatalf("metadata = %+v", result.Metadata)
	}
}

func TestLocationSearchURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		location string
		rent     bool
		want     string
	}{
		{location: "94044", rent: true, want: "https://www.zillow.com/94044/rentals/"},
		{location: "Pacifica, CA", rent: true, want: "https://www.zillow.com/Pacifica-CA/rentals/"},
		{location: "Seattle, WA", want: "https://www.zillow.com/homes/Seattle-WA_rb/"},
	}
	for _, test := range tests {
		got, err := locationSearchURL(test.location, test.rent)
		if err != nil {
			t.Fatalf("locationSearchURL(%q) error = %v", test.location, err)
		}
		if got != test.want {
			t.Fatalf("locationSearchURL(%q, %t) = %q, want %q", test.location, test.rent, got, test.want)
		}
	}
}

func TestNewZillowTransportRequiresTLSProfile(t *testing.T) {
	t.Parallel()

	if _, err := newZillowTransport("test", zillowTransportOptions{Timeout: time.Second}); err == nil || !strings.Contains(err.Error(), "tls-client profile is required") {
		t.Fatalf("newZillowTransport() error = %v", err)
	}
}

func TestNewZillowTransportRejectsUnsupportedProxyScheme(t *testing.T) {
	t.Parallel()

	if _, err := newZillowTransport("test", zillowTransportOptions{Timeout: time.Second, TLSProfile: "default", ProxyURL: "file:///tmp/proxy"}); err == nil {
		t.Fatal("newZillowTransport() error = nil for unsupported proxy scheme")
	}
}

func TestSessionCommandsImportInspectListAndRemove(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("owner-only plaintext sessions are unsupported on Windows")
	}
	t.Setenv("GOZILLO_CONFIG_DIR", t.TempDir())
	harPath := filepath.Join(t.TempDir(), "raw.har")
	if err := os.WriteFile(harPath, []byte(testHARDocument), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := Execute([]string{"session", "import", "--name", "test", harPath}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("session import code = %d, stderr = %q", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "secret-session") {
		t.Fatalf("session import leaked cookie value: %q", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Execute([]string{"--output=json", "session", "inspect", "--name", "test"}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("session inspect code = %d, stderr = %q", code, stderr.String())
	}
	if strings.Contains(stdout.String(), "secret-session") || !strings.Contains(stdout.String(), "zgsession") {
		t.Fatalf("session inspect output = %q", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Execute([]string{"session", "list"}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("session list code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "test") || strings.Contains(stdout.String(), "secret-session") {
		t.Fatalf("session list output = %q", stdout.String())
	}

	stdout.Reset()
	stderr.Reset()
	if code := Execute([]string{"session", "remove", "--name", "test"}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("session remove code = %d, stderr = %q", code, stderr.String())
	}
}

func TestParseLocationsSupportsCommasRepeatsAndDeduplication(t *testing.T) {
	t.Parallel()

	got, err := parseLocations([]string{"94044, 94501", "94502", "94044", "El Cerrito,94530"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"94044", "94501", "94502", "El Cerrito", "94530"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("parseLocations() = %#v, want %#v", got, want)
	}
	if _, err := parseLocations([]string{"94044,,94501"}); err == nil {
		t.Fatal("parseLocations() accepted an empty comma-separated value")
	}
}

func TestMultiLocationTableIncludesSourceArea(t *testing.T) {
	t.Parallel()

	price := int64(3500)
	result := multiLocationSearchResult{Results: []locationSearchResult{
		{Location: "94044", Listings: []zillow.Listing{{ID: "1", Price: &price, Address: zillow.Address{Full: "1 Main St"}}}},
		{Location: "94501", Listings: []zillow.Listing{{ID: "2", Price: &price, Address: zillow.Address{Full: "2 Main St"}}}},
	}}
	table := multiLocationTable(result)
	if !reflect.DeepEqual(table.Headers[:2], []string{"AREA", "ZPID"}) {
		t.Fatalf("headers = %#v", table.Headers)
	}
	if len(table.Rows) != 2 || table.Rows[0][0] != "94044" || table.Rows[1][0] != "94501" {
		t.Fatalf("rows = %#v", table.Rows)
	}
}

func TestNetworkSourcesRequireTLSProfile(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
	}{
		{name: "location search", args: []string{"search", "--location", "Example City ST"}},
		{name: "direct profile", args: []string{"search", "--profile", "missing.profile.json"}},
		{name: "snapshot enrichment", args: []string{"search", "--snapshot", "missing.snapshot.json", "--enrich-details"}},
		{name: "property URL", args: []string{"property", "https://www.zillow.com/example/123_zpid/"}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			if code := Execute(test.args, &stdout, &stderr); code != ExitUsage {
				t.Fatalf("Execute() code = %d, stderr = %q", code, stderr.String())
			}
			if !strings.Contains(stderr.String(), "--tls-profile is required") {
				t.Fatalf("Execute() stderr = %q", stderr.String())
			}
		})
	}
}

func TestOfflineSourcesRejectTLSClientNetworkOptions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "search snapshot",
			args: []string{"search", "--snapshot", "missing.json", "--tls-profile", "chrome_146"},
			want: "only used with --snapshot when detail enrichment is enabled",
		},
		{
			name: "property file",
			args: []string{"property", "--tls-profile", "chrome_146", "missing.html"},
			want: "only valid for URL input",
		},
		{
			name: "search snapshot browser header",
			args: []string{"search", "--snapshot", "missing.json", "--browser-header", "Sec-Fetch-Dest: document"},
			want: "only used with --snapshot when detail enrichment is enabled",
		},
		{
			name: "property file browser header",
			args: []string{"property", "--browser-header", "Sec-Fetch-Dest: document", "missing.html"},
			want: "only valid for URL input",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var stdout bytes.Buffer
			var stderr bytes.Buffer
			if code := Execute(test.args, &stdout, &stderr); code != ExitUsage {
				t.Fatalf("Execute() code = %d, stderr = %q", code, stderr.String())
			}
			if !strings.Contains(stderr.String(), test.want) {
				t.Fatalf("Execute() stderr = %q, want substring %q", stderr.String(), test.want)
			}
		})
	}
}
