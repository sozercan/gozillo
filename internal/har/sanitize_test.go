package har

import (
	"bytes"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
)

func TestSanitizeGoldenAndIdempotent(t *testing.T) {
	t.Parallel()

	original := loadSynthetic(t)
	clean, err := Sanitize(original)
	if err != nil {
		t.Fatalf("Sanitize() error = %v", err)
	}

	var output bytes.Buffer
	if err := Save(&output, clean); err != nil {
		t.Fatalf("Save(sanitized) error = %v", err)
	}
	assertGolden(t, "sanitized.golden.har", output.Bytes())

	if original.Log.Entries[2].Response.Content.Text == "" {
		t.Fatal("Sanitize() mutated the input response body")
	}
	if got := headerValue(original.Log.Entries[2].Request.Headers, "Authorization"); got == "" {
		t.Fatal("Sanitize() mutated the input Authorization header")
	}

	second, err := Sanitize(clean)
	if err != nil {
		t.Fatalf("Sanitize(sanitized) error = %v", err)
	}
	var secondOutput bytes.Buffer
	if err := Save(&secondOutput, second); err != nil {
		t.Fatalf("Save(second sanitized) error = %v", err)
	}
	if output.String() != secondOutput.String() {
		t.Fatalf("Sanitize() is not idempotent\nfirst:\n%s\nsecond:\n%s", output.String(), secondOutput.String())
	}
}

func TestSanitizeRemovesRequiredSecretsAndKeepsDerivationData(t *testing.T) {
	t.Parallel()

	clean := sanitizeSynthetic(t)
	entry := clean.Log.Entries[2]

	for _, name := range []string{"Cookie", "Set-Cookie", "Authorization", "Proxy-Authorization", "X-CSRF-Token"} {
		if value := headerValue(entry.Request.Headers, name); value != "" {
			t.Fatalf("request header %s survived with value %q", name, value)
		}
		if value := headerValue(entry.Response.Headers, name); value != "" {
			t.Fatalf("response header %s survived with value %q", name, value)
		}
	}
	if len(entry.Request.Cookies) != 0 || len(entry.Response.Cookies) != 0 {
		t.Fatal("cookie arrays survived sanitization")
	}
	if entry.Response.Content.Text != "" || entry.Response.Content.Encoding != "" {
		t.Fatal("response body or encoding survived default sanitization")
	}

	body := entry.Request.PostData.Text
	for _, secret := range []string{"person@example.test", "555-0100"} {
		if strings.Contains(body, secret) {
			t.Fatalf("request body still contains %q", secret)
		}
	}
	for _, required := range []string{`"searchQueryState"`, `"usersSearchTerm":"Synthetic City"`, `"wants"`, `"listResults"`} {
		if !strings.Contains(body, required) {
			t.Fatalf("request body lost derivation data %s", required)
		}
	}
}

func TestSanitizeKeepResponseBodiesRequiresExplicitOption(t *testing.T) {
	t.Parallel()

	clean, err := Sanitize(loadSynthetic(t), SanitizeOptions{KeepResponseBodies: true})
	if err != nil {
		t.Fatalf("Sanitize() error = %v", err)
	}
	if clean.Log.Entries[2].Response.Content.Text == "" {
		t.Fatal("response body was removed despite KeepResponseBodies")
	}
	if len(clean.Log.Entries[2].Response.Cookies) != 0 {
		t.Fatal("KeepResponseBodies unexpectedly retained cookies")
	}
}

func TestSanitizeFailsClosedOnMalformedJSON(t *testing.T) {
	t.Parallel()

	sensitiveKey := "to" + "ken"
	archive := &Archive{Log: Log{
		Version: Version12,
		Creator: Creator{Name: "test", Version: "1"},
		Entries: []Entry{{
			Request: Request{
				Method: "POST",
				URL:    "https://www.zillow.com/example",
				PostData: &PostData{
					MimeType: "application/json",
					Text:     `{"` + sensitiveKey + `":"unterminated}`,
				},
			},
			Response: Response{Status: 200},
		}},
	}}

	_, err := Sanitize(archive)
	if err == nil {
		t.Fatal("Sanitize() error = nil, want malformed JSON error")
	}
	if !strings.Contains(err.Error(), "decode JSON body") {
		t.Fatalf("Sanitize() error = %q, want decode JSON body", err)
	}
}

func TestSanitizeAdditionalSensitiveKey(t *testing.T) {
	t.Parallel()

	archive := &Archive{Log: Log{
		Version: Version12,
		Creator: Creator{Name: "test", Version: "1"},
		Entries: []Entry{{
			Request: Request{
				Method: "PUT",
				URL:    "https://www.zillow.com/async-create-search-page-state",
				PostData: &PostData{
					MimeType: "application/json",
					Text:     `{"searchQueryState":{"customPrivateField":"synthetic","safe":"kept"},"wants":{"cat1":["listResults"]}}`,
				},
			},
			Response: Response{Status: 200},
		}},
	}}

	clean, err := Sanitize(archive, SanitizeOptions{AdditionalSensitiveKeys: []string{"custom_private_field"}})
	if err != nil {
		t.Fatalf("Sanitize() error = %v", err)
	}
	if got := clean.Log.Entries[0].Request.PostData.Text; got != `{"searchQueryState":{"customPrivateField":"[REDACTED]","safe":"kept"},"wants":{"cat1":["listResults"]}}` {
		t.Fatalf("postData text = %s", got)
	}
}

func TestSanitizeRedactsURLBearingResponseMetadata(t *testing.T) {
	t.Parallel()

	tokenName := "to" + "ken"
	apiKeyName := "api_" + "key"
	encodedRedaction := "%5B" + "REDACTED" + "%5D"
	archive := &Archive{Log: Log{
		Version: Version12,
		Creator: Creator{Name: "test", Version: "1"},
		Entries: []Entry{{
			Request: Request{Method: "GET", URL: "https://www.zillow.com/example"},
			Response: Response{
				Status:      302,
				RedirectURL: "https://www.zillow.com/next?" + testQueryPair(tokenName, "synthetic") + "#fragment",
				Headers: []NameValue{{
					Name:  "Location",
					Value: "https://www.zillow.com/next?" + testQueryPair(apiKeyName, "synthetic") + "#fragment",
				}},
			},
		}},
	}}

	clean, err := Sanitize(archive)
	if err != nil {
		t.Fatalf("Sanitize() error = %v", err)
	}
	if got := clean.Log.Entries[0].Response.RedirectURL; got != "https://www.zillow.com/next?"+testQueryPair(tokenName, encodedRedaction) {
		t.Fatalf("redirect URL = %q", got)
	}
	if got := headerValue(clean.Log.Entries[0].Response.Headers, "Location"); got != "https://www.zillow.com/next?"+testQueryPair(apiKeyName, encodedRedaction) {
		t.Fatalf("Location header = %q", got)
	}
}

func TestSanitizeHandlesLiteralQuerySemicolons(t *testing.T) {
	t.Parallel()

	tokenName := "to" + "ken"
	encodedRedaction := "%5B" + "REDACTED" + "%5D"
	archive := &Archive{Log: Log{
		Version: Version12,
		Creator: Creator{Name: "test", Version: "1"},
		Entries: []Entry{{
			Request:  Request{Method: "GET", URL: "https://www.zillow.com/example?a=1;b=2;" + testQueryPair(tokenName, "synthetic")},
			Response: Response{Status: 200},
		}},
	}}

	clean, err := Sanitize(archive)
	if err != nil {
		t.Fatalf("Sanitize() error = %v", err)
	}
	if got := clean.Log.Entries[0].Request.URL; got != "https://www.zillow.com/example?a=1;b=2;"+testQueryPair(tokenName, encodedRedaction) {
		t.Fatalf("sanitized URL = %q", got)
	}
}

func TestSanitizeDropsPageMetadataAndPseudoHeaders(t *testing.T) {
	t.Parallel()

	clean := sanitizeSynthetic(t)
	if len(clean.Log.Pages) != 0 {
		t.Fatalf("pages = %d, want 0", len(clean.Log.Pages))
	}
	if clean.Log.Entries[2].PageRef != "" {
		t.Fatalf("page reference = %q, want empty", clean.Log.Entries[2].PageRef)
	}
	if got := headerValue(clean.Log.Entries[2].Request.Headers, ":path"); got != "" {
		t.Fatalf(":path pseudo-header survived with value %q", got)
	}
}

func TestSanitizeRecognizesPrefixedAPIKeys(t *testing.T) {
	t.Parallel()

	queryKey := "google" + "ApiKey"
	jsonKey := "service" + "ApiKey"
	requestBody, err := json.Marshal(map[string]any{"nested": map[string]any{jsonKey: "synthetic-json-value"}})
	if err != nil {
		t.Fatal(err)
	}
	archive := &Archive{Log: Log{
		Version: Version12,
		Creator: Creator{Name: "test", Version: "1"},
		Entries: []Entry{{
			StartedDateTime: "2026-07-19T12:00:00Z",
			Request: Request{
				Method:      "POST",
				URL:         "https://www.zillow.com/example?" + testQueryPair(queryKey, "synthetic-query-value"),
				HTTPVersion: "HTTP/2",
				Headers: []NameValue{{
					Name:  "X-Goog-Api-Key",
					Value: "synthetic-header-value",
				}},
				PostData: &PostData{
					MimeType: "application/json",
					Text:     string(requestBody),
				},
			},
			Response: Response{Status: 200, HTTPVersion: "HTTP/2", Content: Content{MimeType: "application/json"}},
		}},
	}}

	clean, err := Sanitize(archive)
	if err != nil {
		t.Fatalf("Sanitize() error = %v", err)
	}
	entry := clean.Log.Entries[0]
	if got := headerValue(entry.Request.Headers, "X-Goog-Api-Key"); got != "" {
		t.Fatalf("prefixed API-key header survived with value %q", got)
	}
	if strings.Contains(entry.Request.URL, "synthetic-query-value") {
		t.Fatalf("prefixed API-key query survived: %q", entry.Request.URL)
	}
	if strings.Contains(entry.Request.PostData.Text, "synthetic-json-value") {
		t.Fatalf("prefixed API-key JSON value survived: %s", entry.Request.PostData.Text)
	}
}

func TestSanitizeDropsUnneededCommentsAndCacheMetadata(t *testing.T) {
	t.Parallel()

	archive := &Archive{Log: Log{
		Version: Version12,
		Creator: Creator{Name: "test", Version: "1", Comment: "synthetic creator metadata"},
		Comment: "synthetic log metadata",
		Entries: []Entry{{
			StartedDateTime: "2026-07-19T12:00:00Z",
			Comment:         "synthetic entry metadata",
			Cache:           json.RawMessage(`{"comment":"synthetic cache metadata"}`),
			Timings:         &Timings{Comment: "synthetic timing metadata"},
			Request: Request{
				Method:      "GET",
				URL:         "https://www.zillow.com/",
				HTTPVersion: "HTTP/2",
				Comment:     "synthetic request metadata",
				Headers:     []NameValue{{Name: "Accept", Value: "application/json", Comment: "synthetic header metadata"}},
			},
			Response: Response{
				Status:      200,
				HTTPVersion: "HTTP/2",
				Comment:     "synthetic response metadata",
				Content:     Content{MimeType: "application/json", Comment: "synthetic content metadata"},
			},
		}},
	}}

	clean, err := Sanitize(archive)
	if err != nil {
		t.Fatalf("Sanitize() error = %v", err)
	}
	var output bytes.Buffer
	if err := Save(&output, clean); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if strings.Contains(output.String(), "synthetic ") {
		t.Fatalf("sanitized HAR retained optional comment metadata: %s", output.String())
	}
	if got := string(clean.Log.Entries[0].Cache); got != "{}" {
		t.Fatalf("cache = %s, want {}", got)
	}
}

func TestSanitizeClearsNonJSONGraphQLBody(t *testing.T) {
	t.Parallel()

	archive := &Archive{Log: Log{
		Version: Version12,
		Creator: Creator{Name: "test", Version: "1"},
		Entries: []Entry{{
			Request: Request{
				Method: "POST",
				URL:    "https://www.zillow.com/graphql",
				PostData: &PostData{
					MimeType: "application/graphql",
					Text:     `{viewer{id}}`,
				},
			},
			Response: Response{Status: 200},
		}},
	}}

	clean, err := Sanitize(archive)
	if err != nil {
		t.Fatalf("Sanitize() error = %v", err)
	}
	if got := clean.Log.Entries[0].Request.PostData.Text; got != "" {
		t.Fatalf("GraphQL body survived sanitization: %q", got)
	}
}

func TestSanitizeRedactsSessionPathParameters(t *testing.T) {
	t.Parallel()

	archive := &Archive{Log: Log{
		Version: Version12,
		Creator: Creator{Name: "test", Version: "1"},
		Entries: []Entry{{
			Request: Request{
				Method: "GET",
				URL:    "https://www.zillow.com/app;jsessionid=synthetic/path/token/synthetic-value",
			},
			Response: Response{Status: 200},
		}},
	}}

	clean, err := Sanitize(archive)
	if err != nil {
		t.Fatalf("Sanitize() error = %v", err)
	}
	const want = "https://www.zillow.com/app;jsessionid=%5BREDACTED%5D/path/token/%5BREDACTED%5D"
	if got := clean.Log.Entries[0].Request.URL; got != want {
		t.Fatalf("sanitized URL = %q, want %q", got, want)
	}
}

func TestSanitizeRedactsOpaqueAuthenticationURLParameters(t *testing.T) {
	t.Parallel()

	parameterNames := []string{
		"code",
		"auth_code",
		"authorization_code",
		"oauth_code",
		"oauth2_code",
		"oauth_authorization_code",
		"oauth2_authorization_code",
		"oauth_consumer_key",
		"oauth_nonce",
		"oauth_state",
		"oauth_token",
		"oauth_token_secret",
		"oauth_verifier",
		"code_verifier",
		"subscription-key",
		"Ocp-Apim-Subscription-Key",
		"key",
		"state",
		"session_state",
		"nonce",
		"SaMl-ReSpOnSe",
		"sAmL_rEqUeSt",
		"ReLaY-StAtE",
		"ticket",
		"cas_ticket",
		"login_ticket",
		"proxy_ticket",
		"service_ticket",
	}

	for _, parameterName := range parameterNames {
		parameterName := parameterName
		t.Run(parameterName, func(t *testing.T) {
			t.Parallel()

			queryName := url.QueryEscape(parameterName)
			requestURL := "https://www.zillow.com/" + parameterName + "/opaque-path-value?" + queryName + "=opaque-query-value"
			headerURL := "https://www.zillow.com/callback?" + queryName + "=opaque-header-value"
			archive := &Archive{Log: Log{
				Version: Version12,
				Creator: Creator{Name: "test", Version: "1"},
				Entries: []Entry{{
					Request: Request{
						Method:      "GET",
						URL:         requestURL,
						QueryString: []NameValue{{Name: parameterName, Value: "opaque-query-value"}},
						Headers:     []NameValue{{Name: "Referer", Value: headerURL}},
					},
					Response: Response{
						Status:      302,
						RedirectURL: headerURL,
						Headers:     []NameValue{{Name: "Location", Value: headerURL}},
					},
				}},
			}}

			clean, err := Sanitize(archive)
			if err != nil {
				t.Fatalf("Sanitize() error = %v", err)
			}
			entry := clean.Log.Entries[0]
			urlValues := []struct {
				label string
				value string
			}{
				{label: "request URL", value: entry.Request.URL},
				{label: "referer", value: headerValue(entry.Request.Headers, "Referer")},
				{label: "location", value: headerValue(entry.Response.Headers, "Location")},
				{label: "redirect URL", value: entry.Response.RedirectURL},
			}
			for _, urlValue := range urlValues {
				label, value := urlValue.label, urlValue.value
				if strings.Contains(value, "opaque-") {
					t.Fatalf("%s leaked opaque credential value: %q", label, value)
				}
				parsed, err := url.Parse(value)
				if err != nil {
					t.Fatalf("parse sanitized %s: %v", label, err)
				}
				if got := parsed.Query().Get(parameterName); got != DefaultRedaction {
					t.Fatalf("%s query value = %q, want %q", label, got, DefaultRedaction)
				}
			}
			if got := entry.Request.QueryString[0].Value; got != DefaultRedaction {
				t.Fatalf("HAR queryString value = %q, want %q", got, DefaultRedaction)
			}
			parsedRequest, err := url.Parse(entry.Request.URL)
			if err != nil {
				t.Fatal(err)
			}
			if got, want := parsedRequest.Path, "/"+parameterName+"/"+DefaultRedaction; got != want {
				t.Fatalf("request path = %q, want %q", got, want)
			}
		})
	}
}

func TestSanitizeKeepsCallbackNamesInJSON(t *testing.T) {
	t.Parallel()

	callbackFields := map[string]any{
		"state":         "ordinary-state",
		"session_state": "ordinary-session-state",
		"nonce":         "ordinary-nonce",
		"SAMLResponse":  "ordinary-saml-response",
		"SAMLRequest":   "ordinary-saml-request",
		"RelayState":    "ordinary-relay-state",
	}
	requestBody, err := json.Marshal(map[string]any{
		"searchQueryState": callbackFields,
		"wants":            map[string]any{"cat1": []string{"listResults"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	archive := &Archive{Log: Log{
		Version: Version12,
		Creator: Creator{Name: "test", Version: "1"},
		Entries: []Entry{{
			Request: Request{
				Method: "PUT",
				URL:    "https://www.zillow.com/async-create-search-page-state",
				PostData: &PostData{
					MimeType: "application/json",
					Text:     string(requestBody),
				},
			},
			Response: Response{Status: 200},
		}},
	}}

	clean, err := Sanitize(archive)
	if err != nil {
		t.Fatalf("Sanitize() error = %v", err)
	}
	body, err := decodeJSON(clean.Log.Entries[0].Request.PostData.Text)
	if err != nil {
		t.Fatal(err)
	}
	searchState := body.(map[string]any)["searchQueryState"].(map[string]any)
	for name, want := range callbackFields {
		if got := searchState[name]; got != want {
			t.Fatalf("JSON field %q = %#v, want %#v", name, got, want)
		}
	}
}

func TestSanitizePreservesEscapedPathBoundaries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "encoded slash stays inside sensitive value",
			raw:  "https://www.zillow.com/token/abc%2Fdef",
			want: "https://www.zillow.com/token/%5BREDACTED%5D",
		},
		{
			name: "encoded semicolon stays inside sensitive value",
			raw:  "https://www.zillow.com/token/abc%3Bdef",
			want: "https://www.zillow.com/token/%5BREDACTED%5D",
		},
		{
			name: "other encoded delimiters stay inside sensitive value",
			raw:  "https://www.zillow.com/token/abc%3Fdef%23ghi%3Djkl",
			want: "https://www.zillow.com/token/%5BREDACTED%5D",
		},
		{
			name: "encoded delimiter stays inside matrix value",
			raw:  "https://www.zillow.com/app;jsessionid=abc%3Bdef%2Fghi/next",
			want: "https://www.zillow.com/app;jsessionid=%5BREDACTED%5D/next",
		},
		{
			name: "safe escaped path is preserved",
			raw:  "https://www.zillow.com/homes/alpha%2Fbeta%3Bgamma%3Fdelta%23epsilon",
			want: "https://www.zillow.com/homes/alpha%2Fbeta%3Bgamma%3Fdelta%23epsilon",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			archive := &Archive{Log: Log{
				Version: Version12,
				Creator: Creator{Name: "test", Version: "1"},
				Entries: []Entry{{
					Request:  Request{Method: "GET", URL: test.raw},
					Response: Response{Status: 200},
				}},
			}}

			clean, err := Sanitize(archive)
			if err != nil {
				t.Fatalf("Sanitize() error = %v", err)
			}
			if got := clean.Log.Entries[0].Request.URL; got != test.want {
				t.Fatalf("sanitized URL = %q, want %q", got, test.want)
			}
			second, err := Sanitize(clean)
			if err != nil {
				t.Fatalf("Sanitize(sanitized) error = %v", err)
			}
			if got := second.Log.Entries[0].Request.URL; got != test.want {
				t.Fatalf("second sanitized URL = %q, want %q", got, test.want)
			}
		})
	}
}

func TestSanitizeRedactsEmbeddedJSONString(t *testing.T) {
	t.Parallel()

	authorizationKey := "author" + "ization"
	embedded, err := json.Marshal(map[string]string{authorizationKey: "Bear" + "er synthetic-secret"})
	if err != nil {
		t.Fatal(err)
	}
	requestBody, err := json.Marshal(map[string]any{
		"searchQueryState": map[string]any{"payload": string(embedded)},
		"wants":            map[string]any{"cat1": []string{"listResults"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	archive := &Archive{Log: Log{
		Version: Version12,
		Creator: Creator{Name: "test", Version: "1"},
		Entries: []Entry{{
			Request: Request{
				Method: "PUT",
				URL:    "https://www.zillow.com/async-create-search-page-state",
				PostData: &PostData{
					MimeType: "application/json",
					Text:     string(requestBody),
				},
			},
			Response: Response{Status: 200},
		}},
	}}

	clean, err := Sanitize(archive)
	if err != nil {
		t.Fatalf("Sanitize() error = %v", err)
	}
	sanitizedBody := clean.Log.Entries[0].Request.PostData.Text
	if strings.Contains(sanitizedBody, "synthetic-secret") || !strings.Contains(sanitizedBody, `\"authorization\":\"[REDACTED]\"`) {
		t.Fatalf("embedded JSON was not sanitized: %s", sanitizedBody)
	}
}

func TestSanitizeRemovesCommonCredentialHeaderVariants(t *testing.T) {
	t.Parallel()

	headers := []NameValue{
		{Name: "X-Auth", Value: "synthetic"},
		{Name: "X-Session-ID", Value: "synthetic"},
		{Name: "X-Signature", Value: "synthetic"},
		{Name: "X-JWT", Value: "synthetic"},
		{Name: "X-Sig", Value: "synthetic"},
		{Name: "Accept", Value: "application/json"},
	}
	archive := &Archive{Log: Log{
		Version: Version12,
		Creator: Creator{Name: "test", Version: "1"},
		Entries: []Entry{{
			Request:  Request{Method: "GET", URL: "https://www.zillow.com/", Headers: headers},
			Response: Response{Status: 200},
		}},
	}}

	clean, err := Sanitize(archive)
	if err != nil {
		t.Fatalf("Sanitize() error = %v", err)
	}
	if got := clean.Log.Entries[0].Request.Headers; len(got) != 1 || got[0].Name != "Accept" {
		t.Fatalf("sanitized headers = %#v", got)
	}
}

func TestSanitizeDropsNonSearchJSONBodiesByDefault(t *testing.T) {
	t.Parallel()

	archive := &Archive{Log: Log{
		Version: Version12,
		Creator: Creator{Name: "test", Version: "1"},
		Entries: []Entry{{
			Request: Request{
				Method: "POST",
				URL:    "https://www.zillow.com/graphql",
				PostData: &PostData{
					MimeType: "application/json",
					Text:     `{"query":"query Viewer { viewer { id } }"}`,
				},
			},
			Response: Response{Status: 200},
		}},
	}}

	clean, err := Sanitize(archive)
	if err != nil {
		t.Fatalf("Sanitize() error = %v", err)
	}
	if got := clean.Log.Entries[0].Request.PostData.Text; got != "" {
		t.Fatalf("non-search JSON body survived: %q", got)
	}
}

func TestSanitizeRedactsCredentialLikeQueryStringValues(t *testing.T) {
	t.Parallel()

	jwt := strings.Join([]string{
		"eyJhbGciOiJIUzI1NiJ9",
		"eyJzdWIiOiIxMjM0NTY3ODkwIn0",
		"abcdefghijklmnopqrstuvwxyzABCDE",
	}, ".")
	archive := &Archive{Log: Log{
		Version: Version12,
		Creator: Creator{Name: "test", Version: "1"},
		Entries: []Entry{{
			Request: Request{
				Method:      "GET",
				URL:         "https://www.zillow.com/?payload=" + jwt,
				QueryString: []NameValue{{Name: "payload", Value: jwt}},
			},
			Response: Response{Status: 200},
		}},
	}}

	clean, err := Sanitize(archive)
	if err != nil {
		t.Fatalf("Sanitize() error = %v", err)
	}
	if got := clean.Log.Entries[0].Request.QueryString[0].Value; got != DefaultRedaction {
		t.Fatalf("queryString value = %q, want redaction", got)
	}
	if strings.Contains(clean.Log.Entries[0].Request.URL, jwt) {
		t.Fatalf("request URL retained JWT: %q", clean.Log.Entries[0].Request.URL)
	}
}

func TestSanitizeRedactsStandalonePathAndQueryCredentials(t *testing.T) {
	t.Parallel()

	jwt := strings.Join([]string{
		"eyJhbGciOiJIUzI1NiJ9",
		"eyJzdWIiOiIxMjM0NTY3ODkwIn0",
		"abcdefghijklmnopqrstuvwxyzABCDE",
	}, ".")
	archive := &Archive{Log: Log{
		Version: Version12,
		Creator: Creator{Name: "test", Version: "1"},
		Entries: []Entry{{
			Request:  Request{Method: "GET", URL: "https://www.zillow.com/invite/" + jwt + "?" + jwt},
			Response: Response{Status: 200},
		}},
	}}

	clean, err := Sanitize(archive)
	if err != nil {
		t.Fatalf("Sanitize() error = %v", err)
	}
	url := clean.Log.Entries[0].Request.URL
	if strings.Contains(url, jwt) {
		t.Fatalf("sanitized URL retained JWT: %q", url)
	}
	if !strings.Contains(url, "%5BREDACTED%5D") {
		t.Fatalf("sanitized URL lacks redaction marker: %q", url)
	}
}

func TestSanitizeRejectsPathDelimiterInCustomRedaction(t *testing.T) {
	t.Parallel()

	_, err := Sanitize(loadSynthetic(t), SanitizeOptions{Redaction: "masked/value"})
	if err == nil {
		t.Fatal("Sanitize() error = nil for path-delimited redaction marker")
	}
	if !strings.Contains(err.Error(), "path delimiters") {
		t.Fatalf("Sanitize() error = %q", err)
	}
}

func testQueryPair(name, value string) string {
	return name + "=" + value
}

func TestSanitizeRedactsCredentialURLsStoredInSearchJSONStrings(t *testing.T) {
	t.Parallel()

	key := "to" + "ken"
	callback := "https://example.test/callback?" + testQueryPair(key, "opaque-callback-value") + "#fragment"
	body, err := json.Marshal(map[string]any{
		"searchQueryState": map[string]any{
			"usersSearchTerm": callback,
			"label":           "Visit https://example.test in ordinary prose",
		},
		"wants": map[string]any{"cat1": []string{"listResults"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	archive := &Archive{Log: Log{
		Version: Version12,
		Creator: Creator{Name: "test", Version: "1"},
		Entries: []Entry{{
			Request: Request{
				Method: "PUT",
				URL:    "https://www.zillow.com/async-create-search-page-state",
				PostData: &PostData{
					MimeType: "application/json",
					Text:     string(body),
				},
			},
			Response: Response{Status: 200},
		}},
	}}

	clean, err := Sanitize(archive)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(clean.Log.Entries[0].Request.PostData.Text), &decoded); err != nil {
		t.Fatal(err)
	}
	state := decoded["searchQueryState"].(map[string]any)
	sanitizedURL := state["usersSearchTerm"].(string)
	if strings.Contains(sanitizedURL, "opaque-callback-value") || !strings.Contains(sanitizedURL, "%5BREDACTED%5D") {
		t.Fatalf("URL-valued JSON string was not sanitized: %q", sanitizedURL)
	}
	if got := state["label"]; got != "Visit https://example.test in ordinary prose" {
		t.Fatalf("ordinary prose changed: %#v", got)
	}
}

func TestSanitizeRedactsCredentialInQueryAndFormParameterNames(t *testing.T) {
	t.Parallel()

	credentialName := strings.Join([]string{
		"eyJhbGciOiJIUzI1NiJ9",
		"eyJzdWIiOiIxMjM0NTY3ODkwIn0",
		"abcdefghijklmnopqrstuvwxyzABCDE",
	}, ".")
	archive := &Archive{Log: Log{
		Version: Version12,
		Creator: Creator{Name: "test", Version: "1"},
		Entries: []Entry{{
			Request: Request{
				Method:      "POST",
				URL:         "https://www.zillow.com/?" + url.QueryEscape(credentialName),
				QueryString: []NameValue{{Name: credentialName}},
				PostData: &PostData{
					MimeType: "application/x-www-form-urlencoded",
					Params:   []PostDataParam{{Name: credentialName}},
				},
			},
			Response: Response{Status: 200},
		}},
	}}
	clean, err := Sanitize(archive)
	if err != nil {
		t.Fatal(err)
	}
	entry := clean.Log.Entries[0]
	if entry.Request.QueryString[0].Name != DefaultRedaction {
		t.Fatalf("query parameter name = %q", entry.Request.QueryString[0].Name)
	}
	if entry.Request.PostData.Params[0].Name != DefaultRedaction {
		t.Fatalf("form parameter name = %q", entry.Request.PostData.Params[0].Name)
	}
}

func TestSanitizeFailsClosedOnMalformedEmbeddedJSON(t *testing.T) {
	t.Parallel()

	malformed := `{"` + "to" + `ken":"opaque-value"`
	body, err := json.Marshal(map[string]any{
		"searchQueryState": map[string]any{"payload": malformed},
		"wants":            map[string]any{"cat1": []string{"listResults"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	archive := &Archive{Log: Log{
		Version: Version12,
		Creator: Creator{Name: "test", Version: "1"},
		Entries: []Entry{{
			Request: Request{
				Method: "PUT",
				URL:    "https://www.zillow.com/async-create-search-page-state",
				PostData: &PostData{
					MimeType: "application/json",
					Text:     string(body),
				},
			},
			Response: Response{Status: 200},
		}},
	}}
	clean, err := Sanitize(archive)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(clean.Log.Entries[0].Request.PostData.Text, "opaque-value") {
		t.Fatalf("malformed embedded JSON leaked: %s", clean.Log.Entries[0].Request.PostData.Text)
	}
}

func TestSanitizeRedactsCredentialLikeKeysWithValuesAndNestedContainers(t *testing.T) {
	t.Parallel()

	credentialName := strings.Join([]string{
		"eyJhbGciOiJIUzI1NiJ9",
		"eyJzdWIiOiIxMjM0NTY3ODkwIn0",
		"abcdefghijklmnopqrstuvwxyzABCDE",
	}, ".")
	nestedKey := "co" + "de"
	nested := "https://idp.example/callback?" + testQueryPair(nestedKey, "opaque-nested-value")
	rawURL := "https://www.zillow.com/path?" + url.QueryEscape(credentialName) + "=1&return=" + url.QueryEscape(nested)
	archive := &Archive{Log: Log{
		Version: Version12,
		Creator: Creator{Name: "test", Version: "1"},
		Entries: []Entry{{Request: Request{Method: "GET", URL: rawURL}, Response: Response{Status: 200}}},
	}}
	clean, err := Sanitize(archive)
	if err != nil {
		t.Fatal(err)
	}
	got := clean.Log.Entries[0].Request.URL
	if strings.Contains(got, credentialName) || strings.Contains(got, "opaque-nested-value") {
		t.Fatalf("sanitized URL leaked nested credential data: %q", got)
	}
}

func TestSanitizeRetainedHeadersRequireExactPunctuation(t *testing.T) {
	t.Parallel()

	archive := &Archive{Log: Log{
		Version: Version12,
		Creator: Creator{Name: "test", Version: "1"},
		Entries: []Entry{{
			Request: Request{Method: "GET", URL: "https://www.zillow.com/", Headers: []NameValue{
				{Name: "Accept", Value: "application/json"},
				{Name: "Ac-cept", Value: "opaque-session-value"},
				{Name: "Content--Type", Value: "opaque-session-value"},
			}},
			Response: Response{Status: 200},
		}},
	}}
	clean, err := Sanitize(archive)
	if err != nil {
		t.Fatal(err)
	}
	headers := clean.Log.Entries[0].Request.Headers
	if len(headers) != 1 || !strings.EqualFold(headers[0].Name, "Accept") {
		t.Fatalf("retained headers = %#v", headers)
	}
}
