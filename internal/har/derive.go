package har

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
)

const SearchTemplateVersion = 1

// DeriveOptions selects a specific HAR entry. When EntryIndex is nil, the
// highest-ranked matching Zillow search-state request is used.
type DeriveOptions struct {
	EntryIndex *int
}

// SearchTemplate is the stable, minimal JSON profile consumed by the Zillow
// search client. Field order intentionally matches the serialized contract.
type SearchTemplate struct {
	Version          int                 `json:"version"`
	Endpoint         string              `json:"endpoint"`
	Method           string              `json:"method"`
	Referer          string              `json:"referer"`
	SearchQueryState map[string]any      `json:"searchQueryState"`
	Wants            map[string][]string `json:"wants"`
}

// DeriveSearchTemplate derives a profile from either the selected HAR entry or
// the best-ranked first-party PUT /async-create-search-page-state request.
func DeriveSearchTemplate(archive *Archive, options ...DeriveOptions) (*SearchTemplate, error) {
	if len(options) > 1 {
		return nil, errors.New("derive search template: at most one options value is allowed")
	}
	if err := archive.Validate(); err != nil {
		return nil, fmt.Errorf("derive search template: %w", err)
	}

	var opts DeriveOptions
	if len(options) == 1 {
		opts = options[0]
	}

	entryIndex, err := selectSearchEntry(archive, opts.EntryIndex)
	if err != nil {
		return nil, err
	}
	template, err := deriveSearchTemplateFromEntry(&archive.Log.Entries[entryIndex])
	if err != nil {
		return nil, fmt.Errorf("derive search template: entry %d: %w", entryIndex, err)
	}
	return template, nil
}

// DeriveSearchTemplateAt derives from one known HAR entry index.
func DeriveSearchTemplateAt(archive *Archive, entryIndex int) (*SearchTemplate, error) {
	return DeriveSearchTemplate(archive, DeriveOptions{EntryIndex: &entryIndex})
}

// SaveSearchTemplate writes the required deterministic JSON representation.
func SaveSearchTemplate(w io.Writer, template *SearchTemplate) error {
	if template == nil {
		return errors.New("save search template: template is nil")
	}
	if err := template.Validate(); err != nil {
		return fmt.Errorf("save search template: %w", err)
	}
	if err := saveIndentedJSON(w, template); err != nil {
		return fmt.Errorf("save search template: %w", err)
	}
	return nil
}

// Validate checks the derived profile contract.
func (template *SearchTemplate) Validate() error {
	if template == nil {
		return errors.New("template is nil")
	}
	if template.Version != SearchTemplateVersion {
		return fmt.Errorf("version is %d; want %d", template.Version, SearchTemplateVersion)
	}
	if template.Method != "PUT" {
		return fmt.Errorf("method is %q; want PUT", template.Method)
	}
	if _, err := parseSearchTemplateEndpoint(template.Endpoint); err != nil {
		return fmt.Errorf("endpoint: %w", err)
	}
	if _, err := parseSearchTemplateReferer(template.Referer); err != nil {
		return fmt.Errorf("referer: %w", err)
	}
	if template.SearchQueryState == nil {
		return errors.New("searchQueryState is required")
	}
	if len(template.Wants) == 0 {
		return errors.New("wants must be a non-empty object")
	}
	foundListResults := false
	for group, wants := range template.Wants {
		if strings.TrimSpace(group) == "" {
			return errors.New("wants group name must not be empty")
		}
		if len(wants) == 0 {
			return fmt.Errorf("wants.%s must not be empty", group)
		}
		for index, want := range wants {
			if strings.TrimSpace(want) == "" {
				return fmt.Errorf("wants.%s[%d] must not be empty", group, index)
			}
			if group == "cat1" && want == "listResults" {
				foundListResults = true
			}
		}
	}
	if !foundListResults {
		return errors.New("wants.cat1 must contain listResults")
	}
	return nil
}

func selectSearchEntry(archive *Archive, selected *int) (int, error) {
	if selected != nil {
		if *selected < 0 || *selected >= len(archive.Log.Entries) {
			return 0, fmt.Errorf("derive search template: entry index %d is out of range", *selected)
		}
		if err := validateSearchEntry(&archive.Log.Entries[*selected]); err != nil {
			return 0, fmt.Errorf("derive search template: entry %d: %w", *selected, err)
		}
		return *selected, nil
	}

	candidates, err := RankCandidates(archive)
	if err != nil {
		return 0, fmt.Errorf("derive search template: %w", err)
	}
	for _, candidate := range candidates {
		entry := &archive.Log.Entries[candidate.EntryIndex]
		if validateSearchEntry(entry) == nil {
			return candidate.EntryIndex, nil
		}
	}
	return 0, errors.New("derive search template: no first-party PUT /async-create-search-page-state entry found")
}

func validateSearchEntry(entry *Entry) error {
	if entry == nil {
		return errors.New("entry is nil")
	}
	if entry.Request.Method != "PUT" {
		return fmt.Errorf("method %q is not PUT", entry.Request.Method)
	}
	if _, err := parseSearchTemplateEndpoint(entry.Request.URL); err != nil {
		return fmt.Errorf("request URL: %w", err)
	}
	if _, err := parseSearchTemplateReferer(firstHeaderValue(entry.Request.Headers, "Referer", "Referrer")); err != nil {
		return fmt.Errorf("request referer: %w", err)
	}
	if entry.Request.PostData == nil || strings.TrimSpace(entry.Request.PostData.Text) == "" {
		return errors.New("request postData JSON is required")
	}
	return nil
}

func deriveSearchTemplateFromEntry(entry *Entry) (*SearchTemplate, error) {
	if err := validateSearchEntry(entry); err != nil {
		return nil, err
	}

	value, err := decodeJSON(entry.Request.PostData.Text)
	if err != nil {
		return nil, fmt.Errorf("decode postData JSON: %w", err)
	}
	body, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("postData JSON must be an object")
	}
	redactJSON(body, DefaultRedaction, sensitiveKeyNames)

	searchQueryState, ok := body["searchQueryState"].(map[string]any)
	if !ok {
		return nil, errors.New("postData.searchQueryState must be an object")
	}
	wants, err := decodeWants(body["wants"])
	if err != nil {
		return nil, err
	}

	endpoint, _ := parseSearchTemplateEndpoint(entry.Request.URL)

	referer, err := sanitizeURL(firstHeaderValue(entry.Request.Headers, "Referer", "Referrer"), DefaultRedaction, sensitiveKeyNames)
	if err != nil {
		return nil, fmt.Errorf("sanitize referer: %w", err)
	}

	template := &SearchTemplate{
		Version:          SearchTemplateVersion,
		Endpoint:         endpoint.String(),
		Method:           "PUT",
		Referer:          referer,
		SearchQueryState: searchQueryState,
		Wants:            wants,
	}
	if err := template.Validate(); err != nil {
		return nil, err
	}
	return template, nil
}

func parseSearchTemplateEndpoint(raw string) (*url.URL, error) {
	parsed, err := parseSearchTemplateZillowURL(raw)
	if err != nil {
		return nil, err
	}
	if parsed.Path != searchStatePath || parsed.RawPath != "" {
		return nil, fmt.Errorf("path must be exactly %q", searchStatePath)
	}
	if parsed.RawQuery != "" || parsed.ForceQuery {
		return nil, errors.New("must not contain a query")
	}
	if parsed.Fragment != "" {
		return nil, errors.New("must not contain a fragment")
	}
	return parsed, nil
}

func parseSearchTemplateReferer(raw string) (*url.URL, error) {
	parsed, err := parseSearchTemplateZillowURL(raw)
	if err != nil {
		return nil, err
	}
	if parsed.Fragment != "" {
		return nil, errors.New("must not contain a fragment")
	}
	return parsed, nil
}

func parseSearchTemplateZillowURL(raw string) (*url.URL, error) {
	if raw == "" || strings.TrimSpace(raw) == "" {
		return nil, errors.New("must not be empty")
	}
	if raw != strings.TrimSpace(raw) {
		return nil, errors.New("must not have surrounding whitespace")
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if parsed.Opaque != "" || parsed.Scheme != "https" {
		return nil, errors.New("URL scheme must be https")
	}
	if parsed.User != nil {
		return nil, errors.New("must not contain user information")
	}
	if parsed.Port() != "" || !strings.EqualFold(parsed.Host, parsed.Hostname()) {
		return nil, errors.New("must not contain an explicit port")
	}
	if !isSearchTemplateZillowHost(parsed.Hostname()) {
		return nil, fmt.Errorf("host %q is not allowed", parsed.Hostname())
	}
	return parsed, nil
}

func isSearchTemplateZillowHost(host string) bool {
	switch strings.ToLower(host) {
	case "zillow.com", "www.zillow.com":
		return true
	default:
		return false
	}
}

func decodeWants(value any) (map[string][]string, error) {
	object, ok := value.(map[string]any)
	if !ok {
		return nil, errors.New("postData.wants must be an object")
	}

	wants := make(map[string][]string, len(object))
	for key, rawValues := range object {
		array, ok := rawValues.([]any)
		if !ok {
			return nil, fmt.Errorf("postData.wants.%s must be an array", key)
		}
		values := make([]string, len(array))
		for index, rawValue := range array {
			text, ok := rawValue.(string)
			if !ok {
				return nil, fmt.Errorf("postData.wants.%s[%d] must be a string", key, index)
			}
			values[index] = text
		}
		wants[key] = values
	}
	return wants, nil
}

func firstHeaderValue(headers []NameValue, names ...string) string {
	for _, header := range headers {
		for _, name := range names {
			if strings.EqualFold(header.Name, name) {
				return header.Value
			}
		}
	}
	return ""
}
