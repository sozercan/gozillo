package har

import (
	"bytes"
	"strings"
	"testing"
)

func TestDeriveSearchTemplateGolden(t *testing.T) {
	t.Parallel()

	template, err := DeriveSearchTemplate(sanitizeSynthetic(t))
	if err != nil {
		t.Fatalf("DeriveSearchTemplate() error = %v", err)
	}
	if template.Endpoint != "https://www.zillow.com/async-create-search-page-state" {
		t.Fatalf("endpoint = %q", template.Endpoint)
	}
	if template.Method != "PUT" {
		t.Fatalf("method = %q, want PUT", template.Method)
	}
	if got := template.Wants["cat1"]; len(got) != 1 || got[0] != "listResults" {
		t.Fatalf("cat1 wants = %#v", got)
	}

	var output bytes.Buffer
	if err := SaveSearchTemplate(&output, template); err != nil {
		t.Fatalf("SaveSearchTemplate() error = %v", err)
	}
	assertGolden(t, "search-template.golden.json", output.Bytes())
}

func TestDeriveSearchTemplateSanitizesRawEntry(t *testing.T) {
	t.Parallel()

	template, err := DeriveSearchTemplate(loadSynthetic(t))
	if err != nil {
		t.Fatalf("DeriveSearchTemplate(raw) error = %v", err)
	}
	var output bytes.Buffer
	if err := SaveSearchTemplate(&output, template); err != nil {
		t.Fatalf("SaveSearchTemplate() error = %v", err)
	}
	assertGolden(t, "search-template.golden.json", output.Bytes())
}

func TestDeriveSearchTemplateSelectedEntry(t *testing.T) {
	t.Parallel()

	archive := sanitizeSynthetic(t)
	template, err := DeriveSearchTemplateAt(archive, 2)
	if err != nil {
		t.Fatalf("DeriveSearchTemplateAt(2) error = %v", err)
	}
	if template.Version != SearchTemplateVersion {
		t.Fatalf("version = %d, want %d", template.Version, SearchTemplateVersion)
	}

	_, err = DeriveSearchTemplateAt(archive, 1)
	if err == nil {
		t.Fatal("DeriveSearchTemplateAt(1) error = nil, want non-search entry error")
	}
	if !strings.Contains(err.Error(), "is not PUT") {
		t.Fatalf("DeriveSearchTemplateAt(1) error = %q", err)
	}
}

func TestDeriveSearchTemplateRejectsInvalidBodyShape(t *testing.T) {
	t.Parallel()

	archive := &Archive{Log: Log{
		Version: Version12,
		Creator: Creator{Name: "test", Version: "1"},
		Entries: []Entry{{
			Request: Request{
				Method:  "PUT",
				URL:     "https://www.zillow.com/async-create-search-page-state",
				Headers: []NameValue{{Name: "Referer", Value: "https://www.zillow.com/"}},
				PostData: &PostData{
					MimeType: "application/json",
					Text:     `{"searchQueryState":[],"wants":{"cat1":["listResults"]}}`,
				},
			},
			Response:     Response{Status: 200, Content: Content{MimeType: "application/json"}},
			ResourceType: "xhr",
		}},
	}}

	_, err := DeriveSearchTemplate(archive)
	if err == nil {
		t.Fatal("DeriveSearchTemplate() error = nil, want body-shape error")
	}
	if !strings.Contains(err.Error(), "searchQueryState must be an object") {
		t.Fatalf("DeriveSearchTemplate() error = %q", err)
	}
}

func TestDeriveSearchTemplatePreservesJSONNumbers(t *testing.T) {
	t.Parallel()

	archive := &Archive{Log: Log{
		Version: Version12,
		Creator: Creator{Name: "test", Version: "1"},
		Entries: []Entry{{
			Request: Request{
				Method:  "PUT",
				URL:     "https://www.zillow.com/async-create-search-page-state",
				Headers: []NameValue{{Name: "Referer", Value: "https://www.zillow.com/"}},
				PostData: &PostData{
					MimeType: "application/json",
					Text:     `{"searchQueryState":{"regionSelection":[{"regionId":9007199254740993}]},"wants":{"cat1":["listResults"]}}`,
				},
			},
			Response:     Response{Status: 200, Content: Content{MimeType: "application/json"}},
			ResourceType: "fetch",
		}},
	}}

	template, err := DeriveSearchTemplate(archive)
	if err != nil {
		t.Fatalf("DeriveSearchTemplate() error = %v", err)
	}
	var output bytes.Buffer
	if err := SaveSearchTemplate(&output, template); err != nil {
		t.Fatalf("SaveSearchTemplate() error = %v", err)
	}
	if !strings.Contains(output.String(), "9007199254740993") {
		t.Fatalf("large JSON integer was rounded: %s", output.String())
	}
}

func TestSearchTemplateValidateRejectsNonZillowEndpoint(t *testing.T) {
	t.Parallel()

	template := validSearchTemplateForTest()
	template.Endpoint = "https://www.zillow.com.example.test/async-create-search-page-state"
	if err := template.Validate(); err == nil {
		t.Fatal("Validate() error = nil for lookalike host")
	}
}

func TestSearchTemplateValidateRejectsEndpointQuery(t *testing.T) {
	t.Parallel()

	for _, endpoint := range []string{
		"https://www.zillow.com/async-create-search-page-state?token=value",
		"https://www.zillow.com/async-create-search-page-state?",
		"https://user@www.zillow.com/async-create-search-page-state",
		"https://www.zillow.com/async-create-search-page-state#fragment",
	} {
		template := validSearchTemplateForTest()
		template.Endpoint = endpoint
		if err := template.Validate(); err == nil {
			t.Fatalf("Validate() error = nil for endpoint %q", endpoint)
		}
	}
}

func TestSearchTemplateValidateMatchesZillowClientContract(t *testing.T) {
	t.Parallel()

	for _, endpoint := range []string{
		"https://www.zillow.com/async-create-search-page-state",
		"https://zillow.com/async-create-search-page-state",
	} {
		template := validSearchTemplateForTest()
		template.Endpoint = endpoint
		if err := template.Validate(); err != nil {
			t.Fatalf("Validate() error = %v for supported endpoint %q", err, endpoint)
		}
	}

	tests := []struct {
		name   string
		mutate func(*SearchTemplate)
	}{
		{name: "zero version", mutate: func(template *SearchTemplate) { template.Version = 0 }},
		{name: "future version", mutate: func(template *SearchTemplate) { template.Version = SearchTemplateVersion + 1 }},
		{name: "lowercase method", mutate: func(template *SearchTemplate) { template.Method = "put" }},
		{name: "relative endpoint", mutate: func(template *SearchTemplate) { template.Endpoint = searchStatePath }},
		{name: "subdomain endpoint", mutate: func(template *SearchTemplate) {
			template.Endpoint = "https://api.zillow.com" + searchStatePath
		}},
		{name: "endpoint port", mutate: func(template *SearchTemplate) {
			template.Endpoint = "https://www.zillow.com:443" + searchStatePath
		}},
		{name: "endpoint empty port", mutate: func(template *SearchTemplate) {
			template.Endpoint = "https://www.zillow.com:" + searchStatePath
		}},
		{name: "endpoint trailing slash", mutate: func(template *SearchTemplate) { template.Endpoint += "/" }},
		{name: "endpoint encoded path", mutate: func(template *SearchTemplate) {
			template.Endpoint = "https://www.zillow.com/%61sync-create-search-page-state"
		}},
		{name: "empty referer", mutate: func(template *SearchTemplate) { template.Referer = "" }},
		{name: "referer subdomain", mutate: func(template *SearchTemplate) {
			template.Referer = "https://homes.zillow.com/"
		}},
		{name: "referer port", mutate: func(template *SearchTemplate) {
			template.Referer = "https://www.zillow.com:443/"
		}},
		{name: "referer empty port", mutate: func(template *SearchTemplate) {
			template.Referer = "https://www.zillow.com:/"
		}},
		{name: "referer fragment", mutate: func(template *SearchTemplate) {
			template.Referer = "https://www.zillow.com/#fragment"
		}},
		{name: "missing search state", mutate: func(template *SearchTemplate) { template.SearchQueryState = nil }},
		{name: "missing wants", mutate: func(template *SearchTemplate) { template.Wants = nil }},
		{name: "empty wants", mutate: func(template *SearchTemplate) { template.Wants = map[string][]string{} }},
		{name: "missing cat1", mutate: func(template *SearchTemplate) {
			template.Wants = map[string][]string{"cat2": {"mapResults"}}
		}},
		{name: "empty cat1", mutate: func(template *SearchTemplate) {
			template.Wants = map[string][]string{"cat1": {}}
		}},
		{name: "cat1 without list results", mutate: func(template *SearchTemplate) {
			template.Wants = map[string][]string{"cat1": {"mapResults"}}
		}},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			template := validSearchTemplateForTest()
			test.mutate(template)
			if err := template.Validate(); err == nil {
				t.Fatalf("Validate() error = nil for invalid template: %#v", template)
			}
		})
	}
}

func TestDeriveSearchTemplateRejectsProfilesZillowClientWouldReject(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*Archive)
	}{
		{name: "lowercase method", mutate: func(archive *Archive) { archive.Log.Entries[0].Request.Method = "put" }},
		{name: "subdomain endpoint", mutate: func(archive *Archive) {
			archive.Log.Entries[0].Request.URL = "https://api.zillow.com" + searchStatePath
		}},
		{name: "endpoint port", mutate: func(archive *Archive) {
			archive.Log.Entries[0].Request.URL = "https://www.zillow.com:443" + searchStatePath
		}},
		{name: "endpoint empty port", mutate: func(archive *Archive) {
			archive.Log.Entries[0].Request.URL = "https://www.zillow.com:" + searchStatePath
		}},
		{name: "endpoint trailing slash", mutate: func(archive *Archive) { archive.Log.Entries[0].Request.URL += "/" }},
		{name: "endpoint query", mutate: func(archive *Archive) { archive.Log.Entries[0].Request.URL += "?safe=1" }},
		{name: "endpoint fragment", mutate: func(archive *Archive) { archive.Log.Entries[0].Request.URL += "#fragment" }},
		{name: "missing referer", mutate: func(archive *Archive) { archive.Log.Entries[0].Request.Headers = nil }},
		{name: "referer subdomain", mutate: func(archive *Archive) {
			archive.Log.Entries[0].Request.Headers[0].Value = "https://homes.zillow.com/"
		}},
		{name: "referer port", mutate: func(archive *Archive) {
			archive.Log.Entries[0].Request.Headers[0].Value = "https://www.zillow.com:443/"
		}},
		{name: "referer empty port", mutate: func(archive *Archive) {
			archive.Log.Entries[0].Request.Headers[0].Value = "https://www.zillow.com:/"
		}},
		{name: "referer fragment", mutate: func(archive *Archive) {
			archive.Log.Entries[0].Request.Headers[0].Value = "https://www.zillow.com/#fragment"
		}},
		{name: "empty wants", mutate: func(archive *Archive) {
			archive.Log.Entries[0].Request.PostData.Text = `{"searchQueryState":{},"wants":{}}`
		}},
		{name: "missing cat1", mutate: func(archive *Archive) {
			archive.Log.Entries[0].Request.PostData.Text = `{"searchQueryState":{},"wants":{"cat2":["mapResults"]}}`
		}},
		{name: "cat1 without list results", mutate: func(archive *Archive) {
			archive.Log.Entries[0].Request.PostData.Text = `{"searchQueryState":{},"wants":{"cat1":["mapResults"]}}`
		}},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			archive := validSearchArchiveForTest()
			test.mutate(archive)
			if _, err := DeriveSearchTemplateAt(archive, 0); err == nil {
				t.Fatal("DeriveSearchTemplateAt() error = nil, want validation error")
			}
		})
	}
}

func validSearchTemplateForTest() *SearchTemplate {
	return &SearchTemplate{
		Version:          SearchTemplateVersion,
		Endpoint:         "https://www.zillow.com" + searchStatePath,
		Method:           "PUT",
		Referer:          "https://www.zillow.com/homes/Synthetic-City/?page=1",
		SearchQueryState: map[string]any{},
		Wants:            map[string][]string{"cat1": {"listResults"}},
	}
}

func validSearchArchiveForTest() *Archive {
	return &Archive{Log: Log{
		Version: Version12,
		Creator: Creator{Name: "test", Version: "1"},
		Entries: []Entry{{
			Request: Request{
				Method:  "PUT",
				URL:     "https://www.zillow.com" + searchStatePath,
				Headers: []NameValue{{Name: "Referer", Value: "https://www.zillow.com/"}},
				PostData: &PostData{
					MimeType: "application/json",
					Text:     `{"searchQueryState":{},"wants":{"cat1":["listResults"]}}`,
				},
			},
			Response:     Response{Status: 200, Content: Content{MimeType: "application/json"}},
			ResourceType: "xhr",
		}},
	}}
}

func TestSearchTemplateValidateRejectsMalformedWantsGroups(t *testing.T) {
	t.Parallel()

	base := SearchTemplate{
		Version:          SearchTemplateVersion,
		Endpoint:         "https://www.zillow.com/async-create-search-page-state",
		Method:           "PUT",
		Referer:          "https://www.zillow.com/homes/",
		SearchQueryState: map[string]any{},
		Wants:            map[string][]string{"cat1": {"listResults"}},
	}
	tests := []map[string][]string{
		{"cat1": {"listResults", ""}},
		{"cat1": {"listResults"}, "cat2": {}},
		{"cat1": {"listResults"}, "": {"mapResults"}},
	}
	for _, wants := range tests {
		template := base
		template.Wants = wants
		if err := template.Validate(); err == nil {
			t.Fatalf("Validate() accepted wants %#v", wants)
		}
	}
}
