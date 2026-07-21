package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"strings"
	"time"
	"unicode"

	"gozillo/internal/output"
	"gozillo/internal/zillow"
)

type searchCommand struct{}

type locationSearchResult struct {
	Location string                `json:"location"`
	Metadata zillow.SearchMetadata `json:"metadata"`
	Listings []zillow.Listing      `json:"listings"`
	Raw      json.RawMessage       `json:"raw,omitempty"`
	Error    string                `json:"error,omitempty"`
}

type multiLocationSearchResult struct {
	Results []locationSearchResult `json:"results"`
}

type locatedListing struct {
	Location string          `json:"location"`
	Listing  *zillow.Listing `json:"listing,omitempty"`
	Error    string          `json:"error,omitempty"`
}

func (searchCommand) Name() string { return "search" }
func (searchCommand) Summary() string {
	return "Search Zillow through pure Go HTTP or a saved snapshot"
}

func (searchCommand) Run(ctx Context, args []string) error {
	if wantsHelp(args) {
		writeSearchUsage(ctx.Stdout)
		return nil
	}

	flags := flag.NewFlagSet("search", flag.ContinueOnError)
	flags.SetOutput(ctx.Stderr)
	profilePath := flags.String("profile", "", "path to a derived search profile for direct HTTP")
	snapshotPath := flags.String("snapshot", "", "saved Zillow HTML or raw __NEXT_DATA__ JSON")
	var locationValues stringListFlag
	flags.Var(&locationValues, "location", "location/ZIP; repeat or separate multiple values with commas")
	forRent := flags.Bool("rent", false, "search rentals instead of for-sale listings")
	proxyValue := flags.String("proxy", "", "HTTP/HTTPS/SOCKS5 proxy URL; otherwise use standard proxy environment variables")
	sessionName := flags.String("session", "", "named browser-derived session imported from a raw HAR")
	tlsProfile := flags.String("tls-profile", "", "required tls-client browser profile for network requests")
	userAgent := flags.String("user-agent", "", "explicit User-Agent for network requests")
	var browserHeaderValues stringListFlag
	flags.Var(&browserHeaderValues, "browser-header", "allowlisted browser HTML header as Name: Value; repeat as needed")
	page := flags.Int("page", 0, "results page (1-based; 0 keeps captured value)")
	minPrice := flags.Int64("min-price", 0, "minimum price")
	maxPrice := flags.Int64("max-price", 0, "maximum price")
	minBeds := flags.Float64("min-beds", 0, "minimum bedrooms")
	minBaths := flags.Float64("min-baths", 0, "minimum bathrooms")
	minSqft := flags.Int64("min-sqft", 0, "minimum living area in square feet")
	maxDaysOnZillow := flags.Int64("max-days-on-zillow", -1, "maximum days on Zillow; -1 disables the filter")
	availableFromValue := flags.String("available-from", "", "earliest availability date (YYYY-MM-DD)")
	availableByValue := flags.String("available-by", "", "latest availability date (YYYY-MM-DD)")
	laundryValue := flags.String("laundry", filterAny, "laundry: any, in-unit, hookups, shared, none, or unknown")
	parkingValue := flags.String("parking", filterAny, "parking: any, available, garage, private-garage, none, or unknown")
	petsValue := flags.String("pets", filterAny, "pets: any, allowed, dogs, cats, none, or unknown")
	var flexValues stringListFlag
	flags.Var(&flexValues, "flex", "required flex type; repeat or use commas: den, office, bonus, loft, flex, private-garage")
	unknownLaundryValue := flags.String("unknown-laundry", unknownLaundryExclude, "handling for unknown laundry: exclude or watchlist")
	enrichDetails := flags.Bool("enrich-details", false, "fetch each property page and add normalized rental details")
	detailWorkers := flags.Int("detail-workers", 1, "concurrent property-detail requests (1-8)")
	detailDelay := flags.Duration("detail-delay", 750*time.Millisecond, "minimum delay between property-detail request starts")
	locationDelay := flags.Duration("location-delay", 2*time.Second, "delay between locations in a multi-location search")
	locationRetries := flags.Int("location-retries", 2, "retries after a challenge or rate limit")
	retryBackoff := flags.Duration("retry-backoff", 30*time.Second, "initial cooldown before retrying a challenged location")
	noCache := flags.Bool("no-cache", false, "disable shared search and property caches")
	searchCacheTTL := flags.Duration("search-cache-ttl", time.Hour, "freshness window for cached location results")
	propertyCacheTTL := flags.Duration("property-cache-ttl", 6*time.Hour, "freshness window for cached property details")
	sortValue := flags.String("sort", "", "raw Zillow sort value (direct profile mode only)")
	sortByValue := flags.String("sort-by", "recommended", "local sort: recommended, price-asc, price-desc, newest, beds-desc, or sqft-desc")
	limit := flags.Int("limit", 0, "maximum listings printed (0 prints all returned)")
	timeout := flags.Duration("timeout", zillow.DefaultTimeout, "HTTP request timeout")
	includeRaw := flags.Bool("raw", false, "include the raw JSON response in JSON output")
	if err := flags.Parse(args); err != nil {
		return &usageError{err: err}
	}
	if flags.NArg() != 0 {
		return usagef("search does not accept positional arguments")
	}
	browserHeaders, err := parseBrowserHeaders(browserHeaderValues)
	if err != nil {
		return usagef("search --browser-header: %v", err)
	}

	locations, err := parseLocations(locationValues)
	if err != nil {
		return usagef("search --location: %v", err)
	}
	profileSet := strings.TrimSpace(*profilePath) != ""
	snapshotSet := strings.TrimSpace(*snapshotPath) != ""
	locationSet := len(locations) > 0
	if boolInt(profileSet)+boolInt(snapshotSet)+boolInt(locationSet) != 1 {
		return usagef("search requires exactly one of --profile, --snapshot, or --location")
	}
	if *forRent && !locationSet {
		return usagef("search --rent is only valid with --location")
	}
	if *limit < 0 {
		return usagef("search --limit must be non-negative")
	}
	if *timeout <= 0 {
		return usagef("search --timeout must be positive")
	}
	sortBy, err := normalizeSortBy(*sortByValue)
	if err != nil {
		return usagef("search: %v", err)
	}
	if strings.TrimSpace(*sortValue) != "" && sortBy != sortRecommended {
		return usagef("search --sort and --sort-by cannot be combined")
	}

	availableFrom, err := parseDateFlag(*availableFromValue)
	if err != nil {
		return usagef("--available-from: %v", err)
	}
	availableBy, err := parseDateFlag(*availableByValue)
	if err != nil {
		return usagef("--available-by: %v", err)
	}
	flex, err := parseFlexValues(flexValues)
	if err != nil {
		return usagef("--flex: %v", err)
	}
	detailOptions := detailFilterOptions{
		Enrich:          *enrichDetails,
		Workers:         *detailWorkers,
		Delay:           *detailDelay,
		MinSqft:         *minSqft,
		MaxDaysOnZillow: *maxDaysOnZillow,
		AvailableFrom:   availableFrom,
		AvailableBy:     availableBy,
		Laundry:         normalizeDetailChoice(*laundryValue),
		Parking:         normalizeDetailChoice(*parkingValue),
		Pets:            normalizeDetailChoice(*petsValue),
		Flex:            flex,
		UnknownLaundry:  normalizeDetailChoice(*unknownLaundryValue),
	}
	if err := validateDetailFilterOptions(detailOptions); err != nil {
		return usagef("%v", err)
	}
	retryOptions := retryPolicy{Retries: *locationRetries, Backoff: *retryBackoff}
	if err := retryOptions.validate(); err != nil {
		return usagef("%v", err)
	}
	if *locationDelay < 0 {
		return usagef("location-delay must not be negative")
	}
	if *searchCacheTTL < 0 || *propertyCacheTTL < 0 {
		return usagef("cache TTL values must not be negative")
	}
	if snapshotSet && !detailOptions.needsEnrichment() && (networkOptionsSet(*proxyValue, *sessionName, *tlsProfile, *userAgent) || len(browserHeaders) > 0) {
		return usagef("search --proxy, --session, --tls-profile, --user-agent, and --browser-header are only used with --snapshot when detail enrichment is enabled")
	}
	networkRequired := profileSet || locationSet || detailOptions.needsEnrichment()
	if networkRequired && strings.TrimSpace(*tlsProfile) == "" {
		return usagef("search --tls-profile is required for network requests")
	}

	filters := zillow.SearchFilters{
		Page:     *page,
		MinPrice: *minPrice,
		MaxPrice: *maxPrice,
		MinBeds:  *minBeds,
		MinBaths: *minBaths,
		Sort:     *sortValue,
	}
	if !profileSet {
		if filters.Page != 0 || filters.Sort != "" {
			if snapshotSet {
				return usagef("search --page and --sort require --profile; snapshots contain one captured page")
			}
			return usagef("search --page and --sort are not yet supported with --location")
		}
		if err := validateSnapshotFilters(filters); err != nil {
			if snapshotSet {
				return usagef("search snapshot filters: %v", err)
			}
			return usagef("search location filters: %v", err)
		}
	}

	requestContext := context.Background()
	var client *zillow.Client
	if profileSet || locationSet || detailOptions.needsEnrichment() {
		client, err = newZillowTransport(Version, zillowTransportOptions{
			Timeout:        *timeout,
			ProxyURL:       *proxyValue,
			SessionName:    *sessionName,
			TLSProfile:     *tlsProfile,
			UserAgent:      *userAgent,
			BrowserHeaders: browserHeaders,
		})
		if err != nil {
			return err
		}
	}
	var searchResultCache *diskCache
	var propertyResultCache *diskCache
	if !*noCache {
		searchResultCache, err = defaultDiskCache("search", *searchCacheTTL)
		if err != nil {
			_, _ = fmt.Fprintf(ctx.Stderr, "warning: search cache disabled: %v\n", err)
		}
		propertyResultCache, err = defaultDiskCache("property", *propertyCacheTTL)
		if err != nil {
			_, _ = fmt.Fprintf(ctx.Stderr, "warning: property cache disabled: %v\n", err)
		}
	}
	var enricher *listingDetailEnricher
	if detailOptions.needsEnrichment() {
		enricher = newListingDetailEnricher(client, detailOptions.Workers, detailOptions.Delay, propertyResultCache)
	}
	today := time.Now()
	applySortAndLimit := func(listings []zillow.Listing) []zillow.Listing {
		sortListings(listings, sortBy)
		if *limit > 0 && len(listings) > *limit {
			listings = listings[:*limit]
		}
		return listings
	}
	processListings := func(listings []zillow.Listing) []zillow.Listing {
		listings = filterSnapshotListings(listings, filters)
		if enricher == nil {
			return applySortAndLimit(filterDetailedListings(listings, detailOptions, today))
		}
		if !detailOptions.requiresAmenityDetails() {
			listings = filterDetailedListings(listings, detailOptions, today)
			listings = applySortAndLimit(listings)
			enricher.Enrich(requestContext, listings)
			return listings
		}
		listings = prefilterKnownListingMetadata(listings, detailOptions, today)
		enricher.Enrich(requestContext, listings)
		listings = filterDetailedListings(listings, detailOptions, today)
		return applySortAndLimit(listings)
	}

	var result *zillow.SearchResult
	var multiResult *multiLocationSearchResult
	switch {
	case profileSet:
		profile, loadErr := zillow.LoadSearchProfileFile(*profilePath)
		if loadErr != nil {
			return loadErr
		}
		result, err = client.SearchWithOptions(requestContext, profile, zillow.SearchOptions{
			Filters:    filters,
			IncludeRaw: *includeRaw,
		})
		if err == nil {
			result.Listings = processListings(result.Listings)
			result.Metadata.Returned = len(result.Listings)
		}
	case snapshotSet:
		reader := os.Stdin
		if *snapshotPath != "-" {
			file, openErr := os.Open(*snapshotPath)
			if openErr != nil {
				return fmt.Errorf("open search snapshot: %w", openErr)
			}
			defer file.Close()
			reader = file
		}
		result, err = zillow.ReadSearchSnapshotWithOptions(reader, zillow.SearchSnapshotOptions{IncludeRaw: *includeRaw})
		if err == nil {
			result.Listings = processListings(result.Listings)
			result.Metadata.Returned = len(result.Listings)
		}
	case locationSet:
		areaResults := make([]locationSearchResult, 0, len(locations))
		liveLocationRequests := 0
		for _, location := range locations {
			target, buildErr := locationSearchURL(location, *forRent)
			if buildErr != nil {
				if len(locations) == 1 {
					return buildErr
				}
				areaResults = append(areaResults, locationSearchResult{Location: location, Error: buildErr.Error()})
				continue
			}
			cacheKey := fmt.Sprintf("%s|raw=%t", target, *includeRaw)
			var areaResult *zillow.SearchResult
			if searchResultCache != nil {
				var cached zillow.SearchResult
				if hit, cacheErr := searchResultCache.Load(cacheKey, &cached); cacheErr != nil {
					_, _ = fmt.Fprintf(ctx.Stderr, "warning: location %q cache read failed: %v\n", location, cacheErr)
				} else if hit {
					areaResult = &cached
				}
			}
			var fetchErr error
			if areaResult == nil {
				if liveLocationRequests > 0 {
					if sleepErr := sleepContext(requestContext, *locationDelay); sleepErr != nil {
						return sleepErr
					}
				}
				liveLocationRequests++
				areaResult, fetchErr = fetchWithRetry(requestContext, retryOptions, func() (*zillow.SearchResult, error) {
					return client.FetchSearchPageWithOptions(requestContext, target, zillow.SearchPageOptions{IncludeRaw: *includeRaw})
				})
				if fetchErr == nil && searchResultCache != nil {
					if cacheErr := searchResultCache.Save(cacheKey, areaResult); cacheErr != nil {
						_, _ = fmt.Fprintf(ctx.Stderr, "warning: location %q cache write failed: %v\n", location, cacheErr)
					}
				}
			}
			if fetchErr != nil {
				if len(locations) == 1 {
					return fmt.Errorf("location %q: %w", location, explainZillowError(fetchErr))
				}
				message := fetchErr.Error()
				_, _ = fmt.Fprintf(ctx.Stderr, "warning: location %q failed after %d attempt(s): %s\n", location, retryOptions.Retries+1, message)
				areaResults = append(areaResults, locationSearchResult{Location: location, Error: message})
				continue
			}
			if expectedState := locationStateQualifier(location); expectedState != "" {
				originalCount := len(areaResult.Listings)
				areaResult.Listings = filterListingsByState(areaResult.Listings, expectedState)
				if originalCount > 0 && len(areaResult.Listings) == 0 {
					message := fmt.Sprintf("Zillow returned listings outside requested state %s", expectedState)
					if len(locations) == 1 {
						return errors.New(message)
					}
					areaResults = append(areaResults, locationSearchResult{Location: location, Error: message})
					continue
				}
			}
			areaResult.Listings = processListings(areaResult.Listings)
			areaResult.Metadata.Returned = len(areaResult.Listings)
			areaResults = append(areaResults, locationSearchResult{
				Location: location,
				Metadata: areaResult.Metadata,
				Listings: areaResult.Listings,
				Raw:      areaResult.Raw,
			})
		}
		if len(locations) == 1 {
			result = &zillow.SearchResult{Listings: areaResults[0].Listings, Metadata: areaResults[0].Metadata, Raw: areaResults[0].Raw}
		} else {
			multiResult = &multiLocationSearchResult{Results: areaResults}
		}
	}
	if err != nil {
		return explainZillowError(err)
	}

	printer, err := output.NewPrinter(ctx.Stdout, ctx.OutputMode)
	if err != nil {
		return err
	}
	if multiResult != nil {
		return printMultiLocationResult(printer, ctx.OutputMode, *multiResult)
	}
	switch ctx.OutputMode {
	case output.ModeJSONL:
		for _, listing := range result.Listings {
			if err := printer.Print(listing); err != nil {
				return err
			}
		}
		return nil
	case output.ModeTable:
		return printer.Print(searchTable(result.Listings))
	default:
		return printer.Print(result)
	}
}

func writeSearchUsage(w interface{ Write([]byte) (int, error) }) {
	_, _ = w.Write([]byte(`Usage:
  gozillo [global options] search --location <place> [--rent] [options]
  gozillo [global options] search --profile <file> [options]
  gozillo [global options] search --snapshot <file> [options]

Sources:
  --location <place>       Location/ZIP; repeat or use commas for multiple values
  --rent                   Search rentals; valid with --location
  --profile <file>         Derived profile for best-effort direct HTTP
  --snapshot <file>        Saved HTML or raw __NEXT_DATA__ JSON; use - for stdin
  --session <name>         Browser-derived session imported from a raw HAR
  --proxy <URL>            HTTP/HTTPS/SOCKS5 proxy; otherwise use HTTPS_PROXY
  --tls-profile <name>     Required browser profile for every network request
  --user-agent <value>     Explicit network User-Agent; default is gozillo/<version>
  --browser-header <h:v>   Allowlisted browser HTML header; repeat as needed

Listing filters:
  --min-price <n>          Minimum price
  --max-price <n>          Maximum price
  --min-beds <n>           Minimum bedrooms
  --min-baths <n>          Minimum bathrooms
  --min-sqft <n>           Minimum living area in square feet
  --max-days-on-zillow <n> Maximum days on Zillow; -1 disables the filter
  --available-from <date>  Earliest availability date, YYYY-MM-DD
  --available-by <date>    Latest availability date, YYYY-MM-DD
  --laundry <value>        any, in-unit, hookups, shared, none, or unknown
  --parking <value>        any, available, garage, private-garage, none, or unknown
  --pets <value>           any, allowed, dogs, cats, none, or unknown
  --flex <value>           Required flex type; repeat or use commas: den, office,
                           bonus, loft, flex, private-garage
  --unknown-laundry <mode> exclude (default) or retain as watchlist

Detail enrichment:
  --enrich-details         Fetch each property page and add normalized rental details
  --detail-workers <n>     Concurrent detail requests, 1-8 (default 1)
  --detail-delay <duration> Minimum delay between detail request starts (default 750ms)
                           Laundry, parking, pets, and flex filters enable enrichment automatically
  --location-delay <d>     Delay between locations (default 2s)
  --location-retries <n>   Retries after challenge/rate-limit responses (default 2)
  --retry-backoff <d>      Initial retry cooldown, doubled per attempt (default 30s)
  --search-cache-ttl <d>   Reuse location results across commands (default 1h)
  --property-cache-ttl <d> Reuse property details across commands (default 6h)
  --no-cache               Disable shared disk caches

Result controls:
  --page <n>               Results page; direct profile mode only
  --sort <value>           Raw Zillow sort value; direct profile mode only
  --sort-by <value>        recommended, price-asc, price-desc, newest, beds-desc, or sqft-desc
  --limit <n>              Maximum rows per location; 0 prints all returned
  --timeout <duration>     HTTP timeout per request (default 20s)
  --raw                    Include raw search response JSON in JSON output
`))
}

func parseLocations(values []string) ([]string, error) {
	locations := make([]string, 0)
	seen := make(map[string]struct{})
	for _, value := range values {
		parts := strings.Split(value, ",")
		for _, part := range parts {
			location := strings.TrimSpace(part)
			if location == "" {
				return nil, errors.New("contains an empty comma-separated value")
			}
			key := strings.ToLower(location)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			locations = append(locations, location)
		}
	}
	if len(locations) > 64 {
		return nil, errors.New("supports at most 64 unique locations per command")
	}
	return locations, nil
}

func printMultiLocationResult(printer *output.Printer, mode output.Mode, result multiLocationSearchResult) error {
	switch mode {
	case output.ModeJSONL:
		for _, area := range result.Results {
			if area.Error != "" {
				if err := printer.Print(locatedListing{Location: area.Location, Error: area.Error}); err != nil {
					return err
				}
				continue
			}
			for index := range area.Listings {
				if err := printer.Print(locatedListing{Location: area.Location, Listing: &area.Listings[index]}); err != nil {
					return err
				}
			}
		}
		return nil
	case output.ModeTable:
		return printer.Print(multiLocationTable(result))
	default:
		return printer.Print(result)
	}
}

type listingTableColumns struct {
	Details bool
	Match   bool
}

func multiLocationTable(result multiLocationSearchResult) output.Table {
	columns := listingTableColumns{}
	hasErrors := false
	for _, area := range result.Results {
		columns = mergeListingTableColumns(columns, listingColumns(area.Listings))
		hasErrors = hasErrors || area.Error != ""
	}

	listingHeaders := listingTableHeaders(columns)
	rows := make([][]string, 0)
	for _, area := range result.Results {
		if area.Error != "" {
			row := []string{area.Location}
			if hasErrors {
				row = append(row, area.Error)
			}
			row = append(row, make([]string, len(listingHeaders))...)
			rows = append(rows, row)
			continue
		}
		for _, listing := range area.Listings {
			row := []string{area.Location}
			if hasErrors {
				row = append(row, "")
			}
			row = append(row, listingTableRow(listing, columns)...)
			rows = append(rows, row)
		}
	}
	headers := []string{"AREA"}
	if hasErrors {
		headers = append(headers, "ERROR")
	}
	headers = append(headers, listingHeaders...)
	return output.Table{Headers: headers, Rows: rows}
}
func locationSearchURL(location string, forRent bool) (string, error) {
	slug := locationSlug(location)
	if slug == "" {
		return "", errors.New("search location must contain a letter or number")
	}
	if forRent {
		return "https://www.zillow.com/" + slug + "/rentals/", nil
	}
	return "https://www.zillow.com/homes/" + slug + "_rb/", nil
}

func locationStateQualifier(location string) string {
	fields := strings.Fields(strings.TrimSpace(location))
	if len(fields) < 2 {
		return ""
	}
	state := strings.ToUpper(fields[len(fields)-1])
	if len(state) != 2 {
		return ""
	}
	for _, character := range state {
		if character < 'A' || character > 'Z' {
			return ""
		}
	}
	return state
}

func filterListingsByState(listings []zillow.Listing, state string) []zillow.Listing {
	if state == "" {
		return listings
	}
	filtered := make([]zillow.Listing, 0, len(listings))
	for _, listing := range listings {
		if strings.EqualFold(strings.TrimSpace(listing.Address.State), state) {
			filtered = append(filtered, listing)
		}
	}
	return filtered
}

func locationSlug(location string) string {
	var slug strings.Builder
	separatorPending := false
	for _, character := range strings.TrimSpace(location) {
		if unicode.IsLetter(character) || unicode.IsNumber(character) {
			if separatorPending && slug.Len() > 0 {
				slug.WriteByte('-')
			}
			separatorPending = false
			slug.WriteRune(character)
			continue
		}
		separatorPending = true
	}
	return strings.Trim(slug.String(), "-")
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func validateSnapshotFilters(filters zillow.SearchFilters) error {
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
	return nil
}

func filterSnapshotListings(listings []zillow.Listing, filters zillow.SearchFilters) []zillow.Listing {
	filtered := make([]zillow.Listing, 0, len(listings))
	for _, listing := range listings {
		if filters.MinPrice > 0 && (listing.Price == nil || *listing.Price < filters.MinPrice) {
			continue
		}
		if filters.MaxPrice > 0 && (listing.Price == nil || *listing.Price > filters.MaxPrice) {
			continue
		}
		if filters.MinBeds > 0 && (listing.Bedrooms == nil || *listing.Bedrooms < filters.MinBeds) {
			continue
		}
		if filters.MinBaths > 0 && (listing.Bathrooms == nil || *listing.Bathrooms < filters.MinBaths) {
			continue
		}
		filtered = append(filtered, listing)
	}
	return filtered
}

func searchTable(listings []zillow.Listing) output.Table {
	columns := listingColumns(listings)
	rows := make([][]string, 0, len(listings))
	for _, listing := range listings {
		rows = append(rows, listingTableRow(listing, columns))
	}
	return output.Table{Headers: listingTableHeaders(columns), Rows: rows}
}

func listingTableHeaders(columns listingTableColumns) []string {
	headers := []string{"ZPID", "PRICE", "BEDS", "BATHS", "SQFT", "DAYS", "AVAILABLE"}
	if columns.Details {
		headers = append(headers, "YEAR", "LAUNDRY", "PARKING", "PETS", "FLEX")
	}
	if columns.Match {
		headers = append(headers, "MATCH")
	}
	return append(headers, "ADDRESS", "URL")
}

func listingTableRow(listing zillow.Listing, columns listingTableColumns) []string {
	row := []string{
		listing.ID,
		formatMoney(listing.Price, listing.PriceText),
		formatFloat(listing.Bedrooms),
		formatFloat(listing.Bathrooms),
		formatInteger(listing.LivingArea),
		formatInteger(listing.DaysOnZillow),
		listing.Availability,
	}
	if columns.Details {
		row = append(row,
			formatPlainInteger(listing.YearBuilt),
			listing.Laundry,
			listing.Parking,
			listing.PetPolicy,
			strings.Join(listing.FlexSpaces, ","),
		)
	}
	if columns.Match {
		row = append(row, listing.MatchStatus)
	}
	return append(row, formatAddress(listing.Address), listing.URL)
}

func listingColumns(listings []zillow.Listing) listingTableColumns {
	columns := listingTableColumns{}
	for _, listing := range listings {
		if listing.DetailStatus != "" || listing.Laundry != "" || listing.Parking != "" || listing.PetPolicy != "" ||
			len(listing.FlexSpaces) > 0 || listing.YearBuilt != nil {
			columns.Details = true
		}
		if listing.MatchStatus != "" {
			columns.Match = true
		}
	}
	return columns
}

func mergeListingTableColumns(left, right listingTableColumns) listingTableColumns {
	return listingTableColumns{Details: left.Details || right.Details, Match: left.Match || right.Match}
}
func explainZillowError(err error) error {
	switch {
	case errors.Is(err, zillow.ErrChallenge):
		return fmt.Errorf("%w; configure a standard proxy or use --snapshot instead of replaying challenge cookies", err)
	case errors.Is(err, zillow.ErrRateLimited):
		return fmt.Errorf("%w; wait before making another request", err)
	case errors.Is(err, zillow.ErrSchemaDrift):
		return fmt.Errorf("%w; refresh the browser capture; for searches, derive a new profile", err)
	default:
		return err
	}
}

func wantsHelp(args []string) bool {
	for _, arg := range args {
		if arg == "-h" || arg == "--help" {
			return true
		}
	}
	return false
}

func usagef(format string, args ...any) error {
	return &usageError{err: fmt.Errorf(format, args...)}
}
