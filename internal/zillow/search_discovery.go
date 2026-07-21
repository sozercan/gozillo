package zillow

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const maxDiscoveryPages = 20

// SearchRoute is one server-side Zillow query used during location discovery.
type SearchRoute struct {
	Name     string        `json:"name"`
	Filters  SearchFilters `json:"-"`
	MaxPages int           `json:"maxPages,omitempty"`
}

// DiscoveryOptions controls bounded, paginated discovery for one location.
type DiscoveryOptions struct {
	Routes    []SearchRoute
	MaxPages  int
	PageDelay time.Duration
}

// DiscoveryIssue records one failed route page while retaining other results.
type DiscoveryIssue struct {
	Route string `json:"route"`
	Page  int    `json:"page"`
	Error string `json:"error"`
}

// SearchCoverage summarizes how much of one server-side route was fetched.
type SearchCoverage struct {
	Route          string `json:"route"`
	PagesFetched   int    `json:"pagesFetched"`
	TotalPages     int    `json:"totalPages,omitempty"`
	TotalResults   int    `json:"totalResults,omitempty"`
	UniqueListings int    `json:"uniqueListings"`
	Complete       bool   `json:"complete"`
}

// DiscoveryResult combines deduplicated listings with route-level coverage.
type DiscoveryResult struct {
	Listings []Listing        `json:"listings"`
	Coverage []SearchCoverage `json:"coverage"`
	Issues   []DiscoveryIssue `json:"issues,omitempty"`
}

// DiscoverLocation bootstraps a rendered location page, then performs bounded
// server-side routes through Zillow's search-state endpoint.
func (c *Client) DiscoverLocation(ctx context.Context, rawURL string, options DiscoveryOptions) (*DiscoveryResult, error) {
	if c == nil {
		return nil, errors.New("discover Zillow location: client is nil")
	}
	if ctx == nil {
		return nil, errors.New("discover Zillow location: context is nil")
	}
	maxPages, routes, err := validateDiscoveryOptions(options)
	if err != nil {
		return nil, fmt.Errorf("discover Zillow location: %w", err)
	}
	profile, err := c.fetchSearchProfile(ctx, rawURL)
	if err != nil {
		return nil, fmt.Errorf("discover Zillow location: %w", err)
	}

	result := &DiscoveryResult{
		Listings: make([]Listing, 0),
		Coverage: make([]SearchCoverage, 0, len(routes)),
	}
	seen := make(map[string]struct{})
	requestCount := 0
	for _, route := range routes {
		coverage := SearchCoverage{Route: route.Name}
		routeSeen := make(map[string]struct{})
		routeMaxPages := maxPages
		if route.MaxPages > 0 {
			routeMaxPages = route.MaxPages
		}
		for page := 1; page <= routeMaxPages; page++ {
			if requestCount > 0 && options.PageDelay > 0 {
				if err := waitDiscoveryDelay(ctx, options.PageDelay); err != nil {
					return nil, fmt.Errorf("discover Zillow location: %w", err)
				}
			}
			requestCount++
			filters := route.Filters
			filters.Page = page
			pageResult, searchErr := c.SearchWithOptions(ctx, profile, SearchOptions{Filters: filters})
			if searchErr != nil {
				result.Issues = append(result.Issues, DiscoveryIssue{Route: route.Name, Page: page, Error: searchErr.Error()})
				break
			}
			coverage.PagesFetched++
			coverage.TotalPages = pageResult.Metadata.TotalPages
			coverage.TotalResults = pageResult.Metadata.TotalResults
			newOnPage := 0
			for itemIndex, listing := range pageResult.Listings {
				key := listingDiscoveryKey(listing)
				if key == "" {
					key = fmt.Sprintf("%s:%d:%d", route.Name, page, itemIndex)
				}
				if _, exists := routeSeen[key]; !exists {
					routeSeen[key] = struct{}{}
					coverage.UniqueListings++
					newOnPage++
				}
				if _, exists := seen[key]; exists {
					continue
				}
				seen[key] = struct{}{}
				result.Listings = append(result.Listings, listing)
			}
			if pageResult.Metadata.TotalPages > 0 && page >= pageResult.Metadata.TotalPages {
				coverage.Complete = true
				break
			}
			if len(pageResult.Listings) == 0 || newOnPage == 0 {
				coverage.Complete = true
				break
			}
		}
		result.Coverage = append(result.Coverage, coverage)
	}
	return result, nil
}

func (c *Client) fetchSearchProfile(ctx context.Context, rawURL string) (*SearchProfile, error) {
	target, err := parseAllowedZillowURL(rawURL)
	if err != nil {
		return nil, fmt.Errorf("bootstrap search profile: %w", err)
	}
	target.Fragment = ""
	data, _, err := c.execute(ctx, requestSpec{
		operation: "search bootstrap",
		method:    http.MethodGet,
		url:       target,
		kind:      responseHTML,
	})
	if err != nil {
		return nil, err
	}
	nextData, ok := extractNextData(data)
	if !ok {
		return nil, &SchemaDriftError{Operation: "search bootstrap", Path: "script#__NEXT_DATA__", Detail: "required Next.js data script is missing"}
	}
	_, state, err := extractSearchPageState(nextData)
	if err != nil {
		return nil, err
	}
	queryState, ok := nestedSearchQueryState(state)
	if !ok {
		return nil, &SchemaDriftError{Operation: "search bootstrap", Path: "searchPageState.queryState", Detail: "required query state is missing"}
	}
	profile := &SearchProfile{
		Version:          SearchProfileVersion,
		Endpoint:         searchEndpointPath,
		Method:           http.MethodPut,
		Referer:          target.String(),
		SearchQueryState: cloneMap(queryState),
		Wants:            map[string]any{"cat1": []any{"listResults", "mapResults"}},
	}
	profile.SearchQueryState["isListVisible"] = true
	profile.SearchQueryState["isMapVisible"] = false
	if err := profile.Validate(); err != nil {
		return nil, fmt.Errorf("bootstrap search profile: %w", err)
	}
	return profile, nil
}

func nestedSearchQueryState(state map[string]any) (map[string]any, bool) {
	for _, key := range []string{"searchQueryState", "queryState"} {
		if nested, ok := state[key].(map[string]any); ok {
			return nested, true
		}
	}
	return nil, false
}

func validateDiscoveryOptions(options DiscoveryOptions) (int, []SearchRoute, error) {
	maxPages := options.MaxPages
	if maxPages == 0 {
		maxPages = 1
	}
	if maxPages < 1 || maxPages > maxDiscoveryPages {
		return 0, nil, fmt.Errorf("max pages must be between 1 and %d", maxDiscoveryPages)
	}
	if options.PageDelay < 0 {
		return 0, nil, errors.New("page delay must not be negative")
	}
	routes := options.Routes
	if len(routes) == 0 {
		routes = []SearchRoute{{Name: "default"}}
	}
	if len(routes) > 16 {
		return 0, nil, errors.New("supports at most 16 search routes")
	}
	seen := make(map[string]struct{}, len(routes))
	for index := range routes {
		routes[index].Name = strings.TrimSpace(routes[index].Name)
		if routes[index].Name == "" {
			return 0, nil, fmt.Errorf("route %d name must not be empty", index+1)
		}
		if _, exists := seen[routes[index].Name]; exists {
			return 0, nil, fmt.Errorf("duplicate route name %q", routes[index].Name)
		}
		seen[routes[index].Name] = struct{}{}
		if routes[index].MaxPages < 0 || routes[index].MaxPages > maxPages {
			return 0, nil, fmt.Errorf("route %q max pages must be between 1 and the discovery maximum %d", routes[index].Name, maxPages)
		}
		if err := validateSearchFilters(routes[index].Filters); err != nil {
			return 0, nil, fmt.Errorf("route %q: %w", routes[index].Name, err)
		}
	}
	return maxPages, append([]SearchRoute(nil), routes...), nil
}

func waitDiscoveryDelay(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func listingDiscoveryKey(listing Listing) string {
	if id := strings.TrimSpace(listing.ID); id != "" {
		return "id:" + id
	}
	if rawURL := strings.TrimSpace(listing.URL); rawURL != "" {
		return "url:" + rawURL
	}
	if address := strings.TrimSpace(listing.Address.Full); address != "" {
		return "address:" + strings.ToLower(address)
	}
	return ""
}
