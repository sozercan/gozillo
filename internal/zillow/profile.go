package zillow

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"strings"
)

const (
	// SearchProfileVersion is the only profile schema version supported by this client.
	SearchProfileVersion = 1
	searchEndpointPath   = "/async-create-search-page-state"
)

// SearchProfile is the sanitized, reusable portion of a captured Zillow search.
type SearchProfile struct {
	Version          int            `json:"version"`
	Endpoint         string         `json:"endpoint"`
	Method           string         `json:"method"`
	Referer          string         `json:"referer"`
	SearchQueryState map[string]any `json:"searchQueryState"`
	Wants            map[string]any `json:"wants"`
}

// SearchFilters contains optional mutations to a profile's searchQueryState.
// Zero numeric values leave the corresponding captured value unchanged.
type SearchFilters struct {
	Page     int
	MinPrice int64
	MaxPrice int64
	MinBeds  float64
	MinBaths float64
	Sort     string
}

// LoadSearchProfile decodes and validates one strict SearchProfile JSON object.
func LoadSearchProfile(r io.Reader) (*SearchProfile, error) {
	if r == nil {
		return nil, errors.New("load search profile: reader is nil")
	}

	decoder := json.NewDecoder(r)
	decoder.DisallowUnknownFields()
	decoder.UseNumber()

	var profile SearchProfile
	if err := decoder.Decode(&profile); err != nil {
		return nil, fmt.Errorf("load search profile: %w", err)
	}

	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, errors.New("load search profile: multiple JSON values")
		}
		return nil, fmt.Errorf("load search profile: trailing data: %w", err)
	}

	if err := profile.Validate(); err != nil {
		return nil, fmt.Errorf("load search profile: %w", err)
	}
	return &profile, nil
}

// ParseSearchProfile decodes a SearchProfile from bytes.
func ParseSearchProfile(data []byte) (*SearchProfile, error) {
	return LoadSearchProfile(bytes.NewReader(data))
}

// LoadSearchProfileFile loads a profile from a local JSON file.
func LoadSearchProfileFile(path string) (*SearchProfile, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("load search profile file: %w", err)
	}
	defer file.Close()

	profile, err := LoadSearchProfile(file)
	if err != nil {
		return nil, fmt.Errorf("load search profile file %q: %w", path, err)
	}
	return profile, nil
}

// Validate verifies that a profile can only target the supported read-only search operation.
func (p *SearchProfile) Validate() error {
	if p == nil {
		return errors.New("search profile is nil")
	}
	if p.Version != SearchProfileVersion {
		return fmt.Errorf("search profile version must be %d", SearchProfileVersion)
	}
	if !strings.EqualFold(strings.TrimSpace(p.Method), http.MethodPut) {
		return fmt.Errorf("search profile method must be %s", http.MethodPut)
	}

	endpoint, err := resolveSearchEndpoint(p.Endpoint)
	if err != nil {
		return fmt.Errorf("search profile endpoint: %w", err)
	}
	if endpoint.Path != searchEndpointPath || endpoint.RawPath != "" || endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return fmt.Errorf("search profile endpoint path must be exactly %q", searchEndpointPath)
	}

	referer, err := parseAllowedZillowURL(p.Referer)
	if err != nil {
		return fmt.Errorf("search profile referer: %w", err)
	}
	if referer.Fragment != "" {
		return errors.New("search profile referer must not contain a fragment")
	}
	if p.SearchQueryState == nil {
		return errors.New("search profile searchQueryState must be an object")
	}
	if p.Wants == nil || len(p.Wants) == 0 {
		return errors.New("search profile wants must be a non-empty object")
	}
	cat1HasListResults := false
	for group, value := range p.Wants {
		if strings.TrimSpace(group) == "" {
			return errors.New("search profile wants group name must not be empty")
		}
		names, err := validateWantsGroup(value)
		if err != nil {
			return fmt.Errorf("search profile wants.%s: %w", group, err)
		}
		if group == "cat1" {
			for _, name := range names {
				if name == "listResults" {
					cat1HasListResults = true
				}
			}
		}
	}
	if !cat1HasListResults {
		return errors.New("search profile wants.cat1 must contain listResults")
	}
	return nil
}

func validateWantsGroup(value any) ([]string, error) {
	var names []string
	switch items := value.(type) {
	case []any:
		names = make([]string, len(items))
		for index, item := range items {
			name, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("item %d must be a string", index)
			}
			names[index] = name
		}
	case []string:
		names = append([]string(nil), items...)
	default:
		return nil, errors.New("must be an array of strings")
	}
	if len(names) == 0 {
		return nil, errors.New("must not be empty")
	}
	for index, name := range names {
		if strings.TrimSpace(name) == "" {
			return nil, fmt.Errorf("item %d must not be empty", index)
		}
	}
	return names, nil
}

// Clone returns a deep copy that can be safely mutated independently.
func (p *SearchProfile) Clone() *SearchProfile {
	if p == nil {
		return nil
	}

	clone := *p
	clone.SearchQueryState = cloneMap(p.SearchQueryState)
	clone.Wants = cloneMap(p.Wants)
	return &clone
}

// WithFilters clones the profile and applies filters to the clone.
func (p *SearchProfile) WithFilters(filters SearchFilters) (*SearchProfile, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	clone := p.Clone()
	if err := clone.ApplyFilters(filters); err != nil {
		return nil, err
	}
	return clone, nil
}

// ApplySearchFilters clones a profile and applies filters to the clone.
func ApplySearchFilters(profile *SearchProfile, filters SearchFilters) (*SearchProfile, error) {
	return profile.WithFilters(filters)
}

// ApplyFilters mutates only searchQueryState using Zillow's captured field layout.
func (p *SearchProfile) ApplyFilters(filters SearchFilters) error {
	if p == nil {
		return errors.New("apply search filters: profile is nil")
	}
	if err := validateSearchFilters(filters); err != nil {
		return fmt.Errorf("apply search filters: %w", err)
	}
	if p.SearchQueryState == nil {
		return errors.New("apply search filters: searchQueryState is nil")
	}

	if filters.Page > 0 {
		pagination, err := ensureObject(p.SearchQueryState, "pagination")
		if err != nil {
			return fmt.Errorf("apply search filters: %w", err)
		}
		pagination["currentPage"] = filters.Page
	}

	if filters.MinPrice > 0 || filters.MaxPrice > 0 || filters.MinBeds > 0 || filters.MinBaths > 0 || filters.Sort != "" {
		filterState, err := ensureObject(p.SearchQueryState, "filterState")
		if err != nil {
			return fmt.Errorf("apply search filters: %w", err)
		}

		if filters.MinPrice > 0 || filters.MaxPrice > 0 {
			price, err := ensureObject(filterState, "price")
			if err != nil {
				return fmt.Errorf("apply search filters: filterState.%w", err)
			}
			if filters.MinPrice > 0 {
				price["min"] = filters.MinPrice
			}
			if filters.MaxPrice > 0 {
				price["max"] = filters.MaxPrice
			}
		}
		if filters.MinBeds > 0 {
			beds, err := ensureObject(filterState, "beds")
			if err != nil {
				return fmt.Errorf("apply search filters: filterState.%w", err)
			}
			beds["min"] = filters.MinBeds
		}
		if filters.MinBaths > 0 {
			baths, err := ensureObject(filterState, "baths")
			if err != nil {
				return fmt.Errorf("apply search filters: filterState.%w", err)
			}
			baths["min"] = filters.MinBaths
		}
		if filters.Sort != "" {
			sortSelection, err := ensureObject(filterState, "sortSelection")
			if err != nil {
				return fmt.Errorf("apply search filters: filterState.%w", err)
			}
			sortSelection["value"] = filters.Sort
		}
	}

	if err := validateFinalPriceBounds(p.SearchQueryState); err != nil {
		return fmt.Errorf("apply search filters: %w", err)
	}
	return nil
}

func validateFinalPriceBounds(queryState map[string]any) error {
	value, exists := lookupPath(queryState, "filterState", "price")
	if !exists || value == nil {
		return nil
	}
	price, ok := value.(map[string]any)
	if !ok {
		return errors.New("filterState.price must be an object")
	}
	readBound := func(name string) (int64, bool, error) {
		raw, exists := price[name]
		if !exists || raw == nil {
			return 0, false, nil
		}
		bound, ok := int64FromAny(raw)
		if !ok || bound < 0 {
			return 0, false, fmt.Errorf("filterState.price.%s must be a non-negative integer", name)
		}
		return bound, true, nil
	}
	minimum, hasMinimum, err := readBound("min")
	if err != nil {
		return err
	}
	maximum, hasMaximum, err := readBound("max")
	if err != nil {
		return err
	}
	if hasMinimum && hasMaximum && minimum > maximum {
		return errors.New("minimum price must not exceed maximum price after merging captured bounds")
	}
	return nil
}

func validateSearchFilters(filters SearchFilters) error {
	if filters.Page < 0 {
		return errors.New("page must not be negative")
	}
	if filters.MinPrice < 0 || filters.MaxPrice < 0 {
		return errors.New("price filters must not be negative")
	}
	if filters.MinPrice > 0 && filters.MaxPrice > 0 && filters.MinPrice > filters.MaxPrice {
		return errors.New("minimum price must not exceed maximum price")
	}
	if math.IsNaN(filters.MinBeds) || math.IsInf(filters.MinBeds, 0) || math.IsNaN(filters.MinBaths) || math.IsInf(filters.MinBaths, 0) {
		return errors.New("bed and bath filters must be finite")
	}
	if filters.MinBeds < 0 || filters.MinBaths < 0 {
		return errors.New("bed and bath filters must not be negative")
	}
	if filters.Sort != strings.TrimSpace(filters.Sort) {
		return errors.New("sort must not have surrounding whitespace")
	}
	for _, r := range filters.Sort {
		if r < 0x20 || r == 0x7f {
			return errors.New("sort must not contain control characters")
		}
	}
	return nil
}

func ensureObject(parent map[string]any, key string) (map[string]any, error) {
	value, exists := parent[key]
	if !exists || value == nil {
		object := make(map[string]any)
		parent[key] = object
		return object, nil
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s must be an object", key)
	}
	return object, nil
}

func cloneMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	output := make(map[string]any, len(input))
	for key, value := range input {
		output[key] = cloneJSONValue(value)
	}
	return output
}

func cloneJSONValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneMap(typed)
	case []any:
		clone := make([]any, len(typed))
		for index, item := range typed {
			clone[index] = cloneJSONValue(item)
		}
		return clone
	case []string:
		return append([]string(nil), typed...)
	case json.RawMessage:
		return append(json.RawMessage(nil), typed...)
	case []byte:
		return append([]byte(nil), typed...)
	default:
		return value
	}
}

func resolveSearchEndpoint(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("must not be empty")
	}

	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if parsed.IsAbs() {
		return parseAllowedZillowURL(raw)
	}
	if parsed.Host != "" || parsed.Scheme != "" || !strings.HasPrefix(parsed.Path, "/") {
		return nil, errors.New("must be an absolute Zillow URL or absolute path")
	}

	base, _ := url.Parse("https://www.zillow.com")
	return base.ResolveReference(parsed), nil
}

func parseAllowedZillowURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, err
	}
	if parsed.Scheme != "https" {
		return nil, errors.New("URL scheme must be https")
	}
	if parsed.User != nil {
		return nil, errors.New("URL must not contain user information")
	}
	if parsed.Port() != "" {
		return nil, errors.New("URL must not contain an explicit port")
	}
	if !allowedZillowHost(parsed.Hostname()) {
		return nil, &HostNotAllowedError{Host: parsed.Hostname()}
	}
	return parsed, nil
}

func allowedZillowHost(host string) bool {
	switch strings.ToLower(host) {
	case "zillow.com", "www.zillow.com":
		return true
	default:
		return false
	}
}
