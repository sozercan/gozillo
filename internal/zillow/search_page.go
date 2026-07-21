package zillow

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
)

// SearchPageOptions controls ingestion of a rendered Zillow search-results page.
type SearchPageOptions struct {
	IncludeRaw bool
}

// FetchSearchPage downloads a Zillow search-results page and parses its
// __NEXT_DATA__ search state.
func (c *Client) FetchSearchPage(ctx context.Context, rawURL string) (*SearchResult, error) {
	return c.FetchSearchPageWithOptions(ctx, rawURL, SearchPageOptions{})
}

// FetchSearchPageWithOptions downloads and parses a Zillow search-results page.
func (c *Client) FetchSearchPageWithOptions(ctx context.Context, rawURL string, options SearchPageOptions) (*SearchResult, error) {
	if c == nil {
		return nil, errors.New("fetch Zillow search page: client is nil")
	}
	target, err := parseAllowedZillowURL(rawURL)
	if err != nil {
		return nil, fmt.Errorf("fetch Zillow search page: %w", err)
	}
	target.Fragment = ""
	data, _, err := c.execute(ctx, requestSpec{
		operation: "search page",
		method:    http.MethodGet,
		url:       target,
		kind:      responseHTML,
	})
	if err != nil {
		return nil, err
	}
	return ReadSearchSnapshotWithOptions(bytes.NewReader(data), SearchSnapshotOptions{IncludeRaw: options.IncludeRaw})
}
