package cli

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"gozillo/internal/zillow"
)

const maxPreviousResultsBytes = 64 << 20

func readPreviousListings(path string) ([]zillow.Listing, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open previous results: %w", err)
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, maxPreviousResultsBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read previous results: %w", err)
	}
	if len(data) > maxPreviousResultsBytes {
		return nil, errors.New("previous results exceed 64 MiB")
	}
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, errors.New("previous results are empty")
	}

	if listings, ok := decodePreviousDocument(data); ok {
		return listings, nil
	}

	listings := make([]zillow.Listing, 0)
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 64<<10), 8<<20)
	line := 0
	for scanner.Scan() {
		line++
		raw := bytes.TrimSpace(scanner.Bytes())
		if len(raw) == 0 {
			continue
		}
		decoded, ok := decodePreviousRecord(raw)
		if !ok {
			return nil, fmt.Errorf("decode previous results line %d", line)
		}
		listings = append(listings, decoded...)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan previous results: %w", err)
	}
	if len(listings) == 0 {
		return nil, errors.New("previous results contain no listings")
	}
	return listings, nil
}

func decodePreviousDocument(data []byte) ([]zillow.Listing, bool) {
	var array []zillow.Listing
	if json.Unmarshal(data, &array) == nil && len(array) > 0 {
		return array, true
	}
	var search zillow.SearchResult
	if json.Unmarshal(data, &search) == nil && len(search.Listings) > 0 {
		return search.Listings, true
	}
	var multi multiLocationSearchResult
	if json.Unmarshal(data, &multi) == nil && len(multi.Results) > 0 {
		result := make([]zillow.Listing, 0)
		for _, area := range multi.Results {
			result = append(result, area.Listings...)
		}
		if len(result) > 0 {
			return result, true
		}
	}
	return nil, false
}

func decodePreviousRecord(data []byte) ([]zillow.Listing, bool) {
	var wrapper locatedListing
	if json.Unmarshal(data, &wrapper) == nil {
		if wrapper.Listing != nil {
			return []zillow.Listing{*wrapper.Listing}, true
		}
		if strings.TrimSpace(wrapper.Error) != "" {
			return nil, true
		}
	}
	var listing zillow.Listing
	if json.Unmarshal(data, &listing) == nil && listingHasHistoryIdentity(listing) {
		return []zillow.Listing{listing}, true
	}
	return nil, false
}

func listingHasHistoryIdentity(listing zillow.Listing) bool {
	return strings.TrimSpace(listing.ID) != "" || strings.TrimSpace(listing.URL) != "" || strings.TrimSpace(listing.Address.Full) != ""
}
