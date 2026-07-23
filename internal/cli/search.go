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
	strictLocationBoundary := flags.Bool("strict-location-boundary", false, "require exact ZIP or city matches for location results")
	var allowedCityValues stringListFlag
	flags.Var(&allowedCityValues, "allowed-city", "allowed result city; repeat or use commas")
	var locationCityAliasValues stringListFlag
	flags.Var(&locationCityAliasValues, "location-city-alias", "query city alias as QUERY=CITY; repeat as needed")
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
	maxBeds := flags.Float64("max-beds", 0, "maximum bedrooms")
	minBaths := flags.Float64("min-baths", 0, "minimum bathrooms")
	minSqft := flags.Int64("min-sqft", 0, "minimum living area in square feet")
	maxDaysOnZillow := flags.Int64("max-days-on-zillow", -1, "maximum days on Zillow; -1 disables the filter")
	availableFromValue := flags.String("available-from", "", "earliest availability date (YYYY-MM-DD)")
	availableByValue := flags.String("available-by", "", "latest availability date (YYYY-MM-DD)")
	unknownAvailabilityValue := flags.String("unknown-availability", availabilityExclude, "handling for unknown availability: exclude or watchlist")
	outOfWindowAvailabilityValue := flags.String("out-of-window-availability", availabilityExclude, "handling for dates outside the target window: exclude or watchlist")
	maxTotalCost := flags.Int64("max-total-cost", 0, "maximum known monthly cost including required fees")
	verifyRentalStatus := flags.Bool("verify-rental-status", false, "require property-page rental status confirmation")
	excludeSharedHousing := flags.Bool("exclude-shared-housing", false, "exclude room-by-room, co-living, and shared-housing listings")
	excludeStudentHousing := flags.Bool("exclude-student-housing", false, "exclude student housing and dorm-style listings")
	excludeIncomeRestricted := flags.Bool("exclude-income-restricted", false, "exclude listings with household income eligibility limits")
	laundryValue := flags.String("laundry", filterAny, "laundry: any, in-unit, hookups, shared, none, or unknown")
	parkingValue := flags.String("parking", filterAny, "parking: any, available, garage, private-garage, none, or unknown")
	petsValue := flags.String("pets", filterAny, "pets: any, allowed, dogs, cats, none, or unknown")
	var flexValues stringListFlag
	flags.Var(&flexValues, "flex", "required flex type; repeat or use commas: den, office, bonus, loft, flex, private-garage")
	unknownLaundryValue := flags.String("unknown-laundry", unknownLaundryExclude, "handling for unknown laundry: exclude or watchlist")
	enrichDetails := flags.Bool("enrich-details", false, "fetch each property page and add normalized rental details")
	verifyRecency := flags.Bool("verify-recency", false, "fetch expanded unit pages to backfill days/listed/updated dates")
	detailWorkers := flags.Int("detail-workers", 1, "concurrent property-detail requests (1-8)")
	detailDelay := flags.Duration("detail-delay", 750*time.Millisecond, "minimum delay between property-detail request starts")
	locationDelay := flags.Duration("location-delay", 2*time.Second, "delay between locations in a multi-location search")
	maxPages := flags.Int("max-pages", 1, "maximum server-result pages per location route (1-20)")
	pageDelay := flags.Duration("page-delay", 2*time.Second, "delay between server-result page requests")
	var bedRangeValues stringListFlag
	flags.Var(&bedRangeValues, "bed-range", "server bedroom range; repeat or use commas: 2, 3+, 2-4")
	var serverSortValues stringListFlag
	flags.Var(&serverSortValues, "server-sort", "server sort value such as days or mostrecentchange; repeat as needed")
	supplementalNoLaundry := flags.Bool("supplemental-no-laundry", false, "add bounded routes without Zillow's indexed laundry filter")
	supplementalPages := flags.Int("supplemental-pages", 1, "page cap for supplemental and keyword routes")
	var keywordRouteValues stringListFlag
	flags.Var(&keywordRouteValues, "keyword-route", "supplemental exact-2-bedroom Zillow keyword route; repeat as needed")
	var homeTypeValues stringListFlag
	flags.Var(&homeTypeValues, "home-type", "allowed home type; repeat or use commas: apartment, condo, townhouse, single-family")
	var locationPageValues stringListFlag
	flags.Var(&locationPageValues, "location-max-pages", "per-location page cap as LOCATION=PAGES; repeat as needed")
	locationRetries := flags.Int("location-retries", 2, "retries per challenged or rate-limited request")
	retryBackoff := flags.Duration("retry-backoff", 30*time.Second, "initial cooldown before retrying a challenged request")
	noCache := flags.Bool("no-cache", false, "disable shared search and property caches")
	searchCacheTTL := flags.Duration("search-cache-ttl", time.Hour, "freshness window for cached location results")
	propertyCacheTTL := flags.Duration("property-cache-ttl", 6*time.Hour, "freshness window for cached property details")
	sortValue := flags.String("sort", "", "raw Zillow sort value for profile or location discovery")
	sortByValue := flags.String("sort-by", "recommended", "local sort: recommended, price-asc, price-desc, newest, beds-desc, or sqft-desc")
	limit := flags.Int("limit", 0, "maximum listings printed (0 prints all returned)")
	timeout := flags.Duration("timeout", zillow.DefaultTimeout, "HTTP request timeout")
	includeRaw := flags.Bool("raw", false, "include the raw JSON response in JSON output")
	previousResultsPath := flags.String("previous-results", "", "prior JSON/JSONL results for new/changed/still-active labels")
	showProgress := flags.Bool("progress", false, "write line-oriented progress updates to stderr")
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
	allowedCities, err := parseAllowedCities(allowedCityValues)
	if err != nil {
		return usagef("search --allowed-city: %v", err)
	}
	locationCityAliases, err := parseLocationCityAliases(locationCityAliasValues)
	if err != nil {
		return usagef("search --location-city-alias: %v", err)
	}
	boundaryOptions := locationBoundaryOptions{Strict: *strictLocationBoundary, AllowedCities: allowedCities, CityAliases: locationCityAliases}
	profileSet := strings.TrimSpace(*profilePath) != ""
	snapshotSet := strings.TrimSpace(*snapshotPath) != ""
	locationSet := len(locations) > 0
	if boolInt(profileSet)+boolInt(snapshotSet)+boolInt(locationSet) != 1 {
		return usagef("search requires exactly one of --profile, --snapshot, or --location")
	}
	if *forRent && !locationSet {
		return usagef("search --rent is only valid with --location")
	}
	if !locationSet && (*strictLocationBoundary || len(allowedCityValues) > 0 || len(locationCityAliasValues) > 0) {
		return usagef("search location boundary flags require --location")
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
	homeTypes, err := parseHomeTypes(homeTypeValues)
	if err != nil {
		return usagef("--home-type: %v", err)
	}
	detailOptions := detailFilterOptions{
		Enrich:                  *enrichDetails,
		VerifyRecency:           *verifyRecency,
		Workers:                 *detailWorkers,
		Delay:                   *detailDelay,
		MinSqft:                 *minSqft,
		MaxDaysOnZillow:         *maxDaysOnZillow,
		AvailableFrom:           availableFrom,
		AvailableBy:             availableBy,
		UnknownAvailability:     normalizeDetailChoice(*unknownAvailabilityValue),
		OutOfWindowAvailability: normalizeDetailChoice(*outOfWindowAvailabilityValue),
		MaxTotalCost:            *maxTotalCost,
		RequireForRent:          *verifyRentalStatus,
		ExcludeSharedHousing:    *excludeSharedHousing,
		ExcludeStudentHousing:   *excludeStudentHousing,
		ExcludeIncomeRestricted: *excludeIncomeRestricted,
		Laundry:                 normalizeDetailChoice(*laundryValue),
		Parking:                 normalizeDetailChoice(*parkingValue),
		Pets:                    normalizeDetailChoice(*petsValue),
		Flex:                    flex,
		UnknownLaundry:          normalizeDetailChoice(*unknownLaundryValue),
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
	if *pageDelay < 0 {
		return usagef("page-delay must not be negative")
	}
	if *maxPages < 1 || *maxPages > maxLocationDiscoveryPages {
		return usagef("max-pages must be between 1 and %d", maxLocationDiscoveryPages)
	}
	if *supplementalPages < 1 || *supplementalPages > *maxPages {
		return usagef("supplemental-pages must be between 1 and max-pages %d", *maxPages)
	}
	if strings.TrimSpace(*sortValue) != "" && len(serverSortValues) > 0 {
		return usagef("search --sort and --server-sort cannot be combined")
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
		Page:      *page,
		MinPrice:  *minPrice,
		MaxPrice:  *maxPrice,
		MinBeds:   *minBeds,
		MaxBeds:   *maxBeds,
		MinBaths:  *minBaths,
		Sort:      *sortValue,
		HomeTypes: append([]string(nil), homeTypes...),
	}
	discoveryRequested := locationSet && (*maxPages > 1 || len(bedRangeValues) > 0 || len(serverSortValues) > 0 || len(homeTypes) > 0 || len(locationPageValues) > 0 || *supplementalNoLaundry || len(keywordRouteValues) > 0 || filters.Sort != "")
	if !locationSet && (*maxPages != 1 || len(bedRangeValues) > 0 || len(serverSortValues) > 0 || len(locationPageValues) > 0 || *supplementalNoLaundry || len(keywordRouteValues) > 0) {
		return usagef("search discovery and supplemental route flags require --location")
	}
	if snapshotSet && (filters.Page != 0 || filters.Sort != "") {
		return usagef("search --page and --sort require --profile; snapshots contain one captured page")
	}
	if locationSet && filters.Page != 0 {
		return usagef("search --page requires --profile; use --max-pages with --location")
	}
	if !profileSet {
		if err := validateSnapshotFilters(filters); err != nil {
			if snapshotSet {
				return usagef("search snapshot filters: %v", err)
			}
			return usagef("search location filters: %v", err)
		}
	}
	if discoveryRequested && *includeRaw {
		return usagef("search --raw is not supported with paginated location discovery")
	}
	var discoveryOptions zillow.DiscoveryOptions
	var locationPageOverrides map[string]int
	if discoveryRequested {
		discoveryOptions, err = buildLocationDiscoveryOptions(locationDiscoveryConfig{
			MaxPages:              *maxPages,
			PageDelay:             *pageDelay,
			RequestRetries:        retryOptions.Retries,
			RetryBackoff:          retryOptions.Backoff,
			BedRanges:             bedRangeValues,
			ServerSorts:           serverSortValues,
			HomeTypes:             homeTypes,
			ForRent:               *forRent,
			InUnitLaundry:         detailOptions.Laundry == zillow.LaundryInUnit,
			SupplementalNoLaundry: *supplementalNoLaundry,
			SupplementalPages:     *supplementalPages,
			KeywordRoutes:         keywordRouteValues,
			BaseFilters:           filters,
		})
		if err != nil {
			return usagef("search discovery: %v", err)
		}
		locationPageOverrides, err = parseLocationPageOverrides(locationPageValues, *maxPages)
		if err != nil {
			return usagef("search --location-max-pages: %v", err)
		}
		knownLocations := make(map[string]struct{}, len(locations))
		for _, location := range locations {
			knownLocations[strings.ToLower(strings.TrimSpace(location))] = struct{}{}
		}
		for location := range locationPageOverrides {
			if _, exists := knownLocations[location]; !exists {
				return usagef("search --location-max-pages references an unknown location %q", location)
			}
		}
	}
	progress := newSearchProgressLogger(*showProgress, ctx.Stderr)
	if locationSet {
		estimate := estimateMultiLocationSearchPlan(locations, discoveryRequested, discoveryOptions, locationPageOverrides, *locationDelay)
		if discoveryRequested {
			progress.printf("planned %d location(s), %d route(s) per location, up to %d search request(s), and up to %s configured search pacing before network and detail time", len(locations), len(discoveryOptions.Routes), estimate.Requests, estimate.Pacing)
		} else {
			progress.printf("planned %d location(s), up to %d search request(s), and up to %s configured location pacing before network and detail time", len(locations), estimate.Requests, estimate.Pacing)
		}
	}

	var previousListings []zillow.Listing
	if strings.TrimSpace(*previousResultsPath) != "" {
		previousListings, err = readPreviousListings(*previousResultsPath)
		if err != nil {
			return err
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
		if !locationSet {
			enricher.setProgress(func(event detailProgress) {
				progress.details("search", event)
			})
		}
	}
	today := time.Now()
	applySortAndLimit := func(listings []zillow.Listing) []zillow.Listing {
		sortListings(listings, sortBy)
		if *limit > 0 && len(listings) > *limit {
			listings = listings[:*limit]
		}
		return listings
	}
	annotateHistory := func(listings []zillow.Listing) []zillow.Listing {
		if strings.TrimSpace(*previousResultsPath) == "" {
			return listings
		}
		return zillow.AnnotateListingHistory(listings, previousListings)
	}
	filterBasicListings := func(listings []zillow.Listing) []zillow.Listing {
		if discoveryRequested {
			return filterDiscoveredListings(listings, filters)
		}
		return filterSnapshotListings(listings, filters)
	}
	processListings := func(listings []zillow.Listing, afterDetails func([]zillow.Listing) []zillow.Listing) []zillow.Listing {
		listings = filterBasicListings(listings)
		if discoveryRequested {
			listings = filterListingsByDiscoveryBedRanges(listings, discoveryOptions.Routes, enricher != nil)
		}
		if enricher == nil {
			listings = finalizeEnrichedListings(requestContext, nil, listings, detailOptions, today, afterDetails)
			return annotateHistory(applySortAndLimit(listings))
		}
		listings = prepareListingsForDetailEnrichment(listings, detailOptions, today, applySortAndLimit)
		listings = enricher.Enrich(requestContext, listings)
		listings = filterBasicListings(listings)
		if discoveryRequested {
			listings = filterListingsByDiscoveryBedRanges(listings, discoveryOptions.Routes, false)
		}
		listings = finalizeEnrichedListings(requestContext, enricher, listings, detailOptions, today, afterDetails)
		return annotateHistory(applySortAndLimit(listings))
	}

	printer, err := output.NewPrinter(ctx.Stdout, ctx.OutputMode)
	if err != nil {
		return err
	}
	var result *zillow.SearchResult
	var multiResult *multiLocationSearchResult
	streamedMultiLocation := false
	singleLocationPartialError := ""
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
			result.Listings = processListings(result.Listings, nil)
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
			result.Listings = processListings(result.Listings, nil)
			result.Metadata.Returned = len(result.Listings)
		}
	case locationSet:
		areaResults := make([]locationSearchResult, 0, len(locations))
		streamedMultiLocation = len(locations) > 1 && ctx.OutputMode == output.ModeJSONL
		appendAreaResult := func(area locationSearchResult) error {
			if streamedMultiLocation {
				return printLocationResultJSONL(printer, area)
			}
			areaResults = append(areaResults, area)
			return nil
		}
		liveLocationRequests := 0
		for locationIndex, location := range locations {
			locationStarted := time.Now()
			locationLabel := fmt.Sprintf("location %d/%d %q", locationIndex+1, len(locations), location)
			target, buildErr := locationSearchURL(location, *forRent)
			if buildErr != nil {
				if len(locations) == 1 {
					return buildErr
				}
				progress.printf("%s: failed before request: %v", locationLabel, buildErr)
				if err := appendAreaResult(locationSearchResult{Location: location, Error: buildErr.Error()}); err != nil {
					return err
				}
				continue
			}
			locationDiscoveryOptions := applyLocationPageOverride(discoveryOptions, location, locationPageOverrides)
			locationDiscoveryOptions.Progress = func(event zillow.DiscoveryProgress) {
				progress.discovery(locationLabel, event)
			}
			locationEstimate := estimateMultiLocationSearchPlan([]string{location}, discoveryRequested, locationDiscoveryOptions, nil, 0)
			progress.printf("%s: starting (up to %d search request(s))", locationLabel, locationEstimate.Requests)
			cacheKey := searchResultCacheKey(target, *includeRaw, discoveryRequested, locationDiscoveryOptions)
			var areaResult *zillow.SearchResult
			if searchResultCache != nil {
				var cached zillow.SearchResult
				if hit, cacheErr := searchResultCache.Load(cacheKey, &cached); cacheErr != nil {
					_, _ = fmt.Fprintf(ctx.Stderr, "warning: location %q cache read failed: %v\n", location, cacheErr)
				} else if hit {
					areaResult = &cached
					progress.printf("%s: search cache hit with %d candidate(s)", locationLabel, len(cached.Listings))
				}
			}
			var fetchErr error
			cacheableResult := true
			partialError := ""
			locationRetryOptions := wholeLocationRetryPolicy(discoveryRequested, retryOptions)
			locationRetryOptions.OnRetry = func(attempt, maxAttempts int, delay time.Duration, retryErr error) {
				progress.printf("%s: search retry %d/%d in %s after %v", locationLabel, attempt, maxAttempts, delay, retryErr)
			}
			if areaResult == nil {
				if liveLocationRequests > 0 {
					if *locationDelay > 0 {
						progress.printf("%s: waiting %s between live locations", locationLabel, *locationDelay)
					}
					if sleepErr := sleepContext(requestContext, *locationDelay); sleepErr != nil {
						return sleepErr
					}
				}
				liveLocationRequests++
				areaResult, fetchErr = fetchWithRetry(requestContext, locationRetryOptions, func() (*zillow.SearchResult, error) {
					if !discoveryRequested {
						return client.FetchSearchPageWithOptions(requestContext, target, zillow.SearchPageOptions{IncludeRaw: *includeRaw})
					}
					discovered, discoverErr := client.DiscoverLocation(requestContext, target, locationDiscoveryOptions)
					if discoverErr != nil {
						return nil, discoverErr
					}
					if len(discovered.Issues) > 0 {
						cacheableResult = false
						first := discovered.Issues[0]
						partialError = fmt.Sprintf("incomplete discovery: %d route page(s) failed; first failure on route %q page %d: %s", len(discovered.Issues), first.Route, first.Page, first.Error)
					}
					for _, issue := range discovered.Issues {
						_, _ = fmt.Fprintf(ctx.Stderr, "warning: location %q route %q page %d failed: %s\n", location, issue.Route, issue.Page, issue.Error)
					}
					for _, coverage := range discovered.Coverage {
						if !coverage.Complete {
							_, _ = fmt.Fprintf(ctx.Stderr, "warning: location %q route %q stopped after %d of %d reported pages\n", location, coverage.Route, coverage.PagesFetched, coverage.TotalPages)
						}
					}
					if len(discovered.Listings) == 0 && len(discovered.Issues) > 0 {
						return nil, errors.New(discovered.Issues[0].Error)
					}
					return &zillow.SearchResult{Listings: discovered.Listings, Metadata: zillow.SearchMetadata{Returned: len(discovered.Listings)}}, nil
				})
				if fetchErr == nil && cacheableResult && searchResultCache != nil {
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
				if discoveryRequested {
					_, _ = fmt.Fprintf(ctx.Stderr, "warning: location %q failed: %s\n", location, message)
				} else {
					_, _ = fmt.Fprintf(ctx.Stderr, "warning: location %q failed after %d attempt(s): %s\n", location, locationRetryOptions.Retries+1, message)
				}
				progress.printf("%s: failed after %s: %s", locationLabel, time.Since(locationStarted).Round(time.Second), message)
				if err := appendAreaResult(locationSearchResult{Location: location, Error: message}); err != nil {
					return err
				}
				continue
			}
			progress.printf("%s: search returned %d candidate(s)", locationLabel, len(areaResult.Listings))
			if *strictLocationBoundary || len(allowedCities) > 0 {
				areaResult.Listings = filterListingsByLocationBoundary(areaResult.Listings, location, boundaryOptions, true)
			} else if expectedState := locationStateQualifier(location); expectedState != "" {
				originalCount := len(areaResult.Listings)
				areaResult.Listings = filterListingsByState(areaResult.Listings, expectedState)
				if originalCount > 0 && len(areaResult.Listings) == 0 {
					message := fmt.Sprintf("Zillow returned listings outside requested state %s", expectedState)
					if len(locations) == 1 {
						return errors.New(message)
					}
					progress.printf("%s: failed boundary validation: %s", locationLabel, message)
					if err := appendAreaResult(locationSearchResult{Location: location, Error: message}); err != nil {
						return err
					}
					continue
				}
			}
			if enricher != nil {
				// A retryable detail response stops the remainder of this
				// location, but optional recency failures must not suppress
				// required detail checks for later locations.
				enricher.resetPause()
				enricher.setProgress(func(event detailProgress) {
					progress.details(locationLabel, event)
				})
			}
			var afterDetails func([]zillow.Listing) []zillow.Listing
			if *strictLocationBoundary || len(allowedCities) > 0 {
				afterDetails = func(listings []zillow.Listing) []zillow.Listing {
					return filterListingsByLocationBoundary(listings, location, boundaryOptions, false)
				}
			}
			areaResult.Listings = processListings(areaResult.Listings, afterDetails)
			areaResult.Metadata.Returned = len(areaResult.Listings)
			area := locationSearchResult{
				Location: location,
				Metadata: areaResult.Metadata,
				Listings: areaResult.Listings,
				Raw:      areaResult.Raw,
				Error:    partialError,
			}
			if err := appendAreaResult(area); err != nil {
				return err
			}
			if partialError != "" {
				progress.printf("%s: completed incompletely with %d listing(s) in %s: %s", locationLabel, len(area.Listings), time.Since(locationStarted).Round(time.Second), partialError)
			} else {
				progress.printf("%s: completed with %d listing(s) in %s", locationLabel, len(area.Listings), time.Since(locationStarted).Round(time.Second))
			}
		}
		progress.printf("completed %d location(s)", len(locations))
		if len(locations) == 1 {
			singleLocationPartialError = areaResults[0].Error
			result = &zillow.SearchResult{Listings: areaResults[0].Listings, Metadata: areaResults[0].Metadata, Raw: areaResults[0].Raw}
		} else if !streamedMultiLocation {
			multiResult = &multiLocationSearchResult{Results: areaResults}
		}
	}
	if err != nil {
		return explainZillowError(err)
	}

	if streamedMultiLocation {
		return nil
	}
	if multiResult != nil {
		return printMultiLocationResult(printer, ctx.OutputMode, *multiResult)
	}
	return printSingleSearchResult(printer, ctx.OutputMode, result, singleLocationPartialError)
}

func printSingleSearchResult(printer *output.Printer, mode output.Mode, result *zillow.SearchResult, partialError string) error {
	var printErr error
	switch mode {
	case output.ModeJSONL:
		for _, listing := range result.Listings {
			if err := printer.Print(listing); err != nil {
				return err
			}
		}
	case output.ModeTable:
		printErr = printer.Print(searchTable(result.Listings))
	default:
		printErr = printer.Print(result)
	}
	if printErr != nil {
		return printErr
	}
	if partialError != "" {
		return errors.New(partialError)
	}
	return nil
}

func writeSearchUsage(w interface{ Write([]byte) (int, error) }) {
	_, _ = w.Write([]byte(`Usage:
  gozillo [global options] search --location <place> [--rent] [options]
  gozillo [global options] search --profile <file> [options]
  gozillo [global options] search --snapshot <file> [options]

Sources:
  --location <place>       Location/ZIP; repeat or use commas for multiple values
  --strict-location-boundary Require exact ZIP/city result matches
  --allowed-city <name>     Allowed result city; repeat or use commas
  --location-city-alias <q=c> Map a query name to an accepted postal city
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
  --max-beds <n>           Maximum bedrooms
  --min-baths <n>          Minimum bathrooms
  --min-sqft <n>           Minimum living area in square feet
  --max-days-on-zillow <n> Maximum days on Zillow; -1 disables the filter
  --available-from <date>  Earliest availability date, YYYY-MM-DD
  --available-by <date>    Latest availability date, YYYY-MM-DD
  --unknown-availability <mode> exclude or watchlist
  --out-of-window-availability <mode> exclude or watchlist
  --max-total-cost <n>     Maximum monthly cost including known required fees
  --verify-rental-status   Confirm FOR_RENT status from the property page
  --exclude-shared-housing Exclude room-by-room and co-living listings
  --exclude-student-housing Exclude student and dorm-style housing
  --exclude-income-restricted Exclude listings with income eligibility limits
  --laundry <value>        any, in-unit, hookups, shared, none, or unknown
  --parking <value>        any, available, garage, private-garage, none, or unknown
  --pets <value>           any, allowed, dogs, cats, none, or unknown
  --flex <value>           Required flex type; repeat or use commas: den, office,
                           bonus, loft, flex, private-garage
  --unknown-laundry <mode> exclude (default) or retain as watchlist

Detail enrichment:
  --enrich-details         Fetch each property page and add normalized rental details
  --verify-recency         Fetch expanded unit pages for actual recency fields
  --detail-workers <n>     Concurrent detail requests, 1-8 (default 1)
  --detail-delay <duration> Minimum delay between detail request starts (default 750ms)
                           Laundry, parking, pets, and flex filters enable enrichment automatically
  --location-delay <d>     Delay between locations (default 2s)
  --max-pages <n>          Maximum pages per server route, 1-20 (default 1)
  --page-delay <d>         Delay between server-result pages (default 2s)
  --bed-range <range>      Server bedroom route; repeat: 2, 3+, 2-4
  --server-sort <value>    Server sort route; repeat: days, mostrecentchange
  --supplemental-no-laundry Add routes without Zillow's indexed laundry flag
  --supplemental-pages <n> Page cap for supplemental/keyword routes
  --keyword-route <text>  Exact-2-bedroom keyword route; repeat as needed
  --home-type <value>      apartment, condo, townhouse, or single-family
  --location-max-pages <spec> Per-location cap as LOCATION=PAGES
  --location-retries <n>   Retries per challenge/rate-limited request (default 2)
  --retry-backoff <d>      Initial retry cooldown, doubled per attempt (default 30s)
  --search-cache-ttl <d>   Reuse location results across commands (default 1h)
  --property-cache-ttl <d> Reuse property details across commands (default 6h)
  --no-cache               Disable shared disk caches

Result controls:
  --page <n>               Results page; direct profile mode only
  --sort <value>           Raw Zillow sort value; profile or location discovery
  --sort-by <value>        recommended, price-asc, price-desc, newest, beds-desc, or sqft-desc
  --limit <n>              Maximum rows per location; 0 prints all returned
  --timeout <duration>     HTTP timeout per request (default 20s)
  --raw                    Include raw search response JSON in JSON output
  --previous-results <path> Prior JSON/JSONL for new/changed/still-active labels
  --progress               Write line-oriented progress updates to stderr
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
			if err := printLocationResultJSONL(printer, area); err != nil {
				return err
			}
		}
		return nil
	case output.ModeTable:
		return printer.Print(multiLocationTable(result))
	default:
		return printer.Print(result)
	}
}

func printLocationResultJSONL(printer *output.Printer, area locationSearchResult) error {
	for index := range area.Listings {
		if err := printer.Print(locatedListing{Location: area.Location, Listing: &area.Listings[index]}); err != nil {
			return err
		}
	}
	if area.Error != "" {
		return printer.Print(locatedListing{Location: area.Location, Error: area.Error})
	}
	return nil
}

type listingTableColumns struct {
	Details      bool
	Cost         bool
	Match        bool
	History      bool
	Verification bool
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

type semanticDiscoveryCacheOptions struct {
	Routes   []zillow.SearchRoute
	MaxPages int
}

func searchResultCacheKey(target string, includeRaw, discovery bool, options zillow.DiscoveryOptions) string {
	semantic := semanticDiscoveryCacheOptions{}
	if discovery {
		semantic.Routes = options.Routes
		semantic.MaxPages = options.MaxPages
	}
	return fmt.Sprintf("%s|raw=%t|discovery=%t|options=%#v", target, includeRaw, discovery, semantic)
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
	if math.IsNaN(filters.MinBeds) || math.IsInf(filters.MinBeds, 0) || math.IsNaN(filters.MaxBeds) || math.IsInf(filters.MaxBeds, 0) || math.IsNaN(filters.MinBaths) || math.IsInf(filters.MinBaths, 0) {
		return errors.New("bed and bath filters must be finite")
	}
	if filters.MinBeds < 0 || filters.MaxBeds < 0 || filters.MinBaths < 0 {
		return errors.New("bed and bath filters must not be negative")
	}
	if filters.MinBeds > 0 && filters.MaxBeds > 0 && filters.MinBeds > filters.MaxBeds {
		return errors.New("minimum beds must not exceed maximum beds")
	}
	return nil
}

func filterSnapshotListings(listings []zillow.Listing, filters zillow.SearchFilters) []zillow.Listing {
	return filterListingsByBasicMetadata(listings, filters, false)
}

func filterDiscoveredListings(listings []zillow.Listing, filters zillow.SearchFilters) []zillow.Listing {
	// Discovery routes already sent the requested home-type constraints to
	// Zillow. Some valid list cards omit homeType until their property page is
	// loaded, so retain unknown values for enrichment while still rejecting a
	// known disallowed type.
	return filterListingsByBasicMetadata(listings, filters, true)
}

func filterListingsByBasicMetadata(listings []zillow.Listing, filters zillow.SearchFilters, allowUnknownHomeType bool) []zillow.Listing {
	filtered := make([]zillow.Listing, 0, len(listings))
	for _, listing := range listings {
		if filters.MinPrice > 0 {
			if (listing.Price == nil && !listing.IsBuilding) || (listing.Price != nil && *listing.Price < filters.MinPrice) {
				continue
			}
		}
		if filters.MaxPrice > 0 {
			if (listing.Price == nil && !listing.IsBuilding) || (listing.Price != nil && *listing.Price > filters.MaxPrice) {
				continue
			}
		}
		if !listing.IsBuilding {
			if filters.MinBeds > 0 && (listing.Bedrooms == nil || *listing.Bedrooms < filters.MinBeds) {
				continue
			}
			if filters.MaxBeds > 0 && (listing.Bedrooms == nil || *listing.Bedrooms > filters.MaxBeds) {
				continue
			}
			if filters.MinBaths > 0 && (listing.Bathrooms == nil || *listing.Bathrooms < filters.MinBaths) {
				continue
			}
		}
		if len(filters.HomeTypes) > 0 {
			homeType := strings.TrimSpace(listing.HomeType)
			if homeType == "" && allowUnknownHomeType {
				filtered = append(filtered, listing)
				continue
			}
			if !listingHomeTypeMatches(homeType, filters.HomeTypes) {
				continue
			}
		}
		filtered = append(filtered, listing)
	}
	return filtered
}

func listingHomeTypeMatches(value string, allowed []string) bool {
	normalized := strings.ToUpper(strings.TrimSpace(value))
	for _, candidate := range allowed {
		switch candidate {
		case zillow.HomeTypeApartment:
			if normalized == "APARTMENT" {
				return true
			}
		case zillow.HomeTypeCondo:
			if normalized == "CONDO" || normalized == "CONDOMINIUM" || normalized == "CONDO_COOP" || normalized == "COOP" {
				return true
			}
		case zillow.HomeTypeTownhouse:
			if normalized == "TOWNHOUSE" || normalized == "TOWNHOME" {
				return true
			}
		case zillow.HomeTypeSingleFamily:
			if normalized == "SINGLE_FAMILY" || normalized == "HOUSE" {
				return true
			}
		}
	}
	return false
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
	headers := []string{"ZPID", "PRICE"}
	if columns.Cost {
		headers = append(headers, "FEES", "TOTAL")
	}
	headers = append(headers, "BEDS", "BATHS", "SQFT", "DAYS", "AVAILABLE")
	if columns.Details {
		headers = append(headers, "YEAR", "LAUNDRY", "PARKING", "PETS", "FLEX")
	}
	if columns.Match {
		headers = append(headers, "MATCH")
	}
	if columns.History {
		headers = append(headers, "HISTORY", "CHANGES")
	}
	if columns.Verification {
		headers = append(headers, "VERIFY")
	}
	return append(headers, "ADDRESS", "URL")
}

func listingTableRow(listing zillow.Listing, columns listingTableColumns) []string {
	row := []string{
		listing.ID,
		formatMoney(listing.Price, listing.PriceText),
	}
	if columns.Cost {
		row = append(row, formatMoney(listing.RequiredMonthlyFees, ""), formatMoney(listing.TotalMonthlyCost, ""))
	}
	row = append(row,
		formatFloat(listing.Bedrooms),
		formatFloat(listing.Bathrooms),
		formatInteger(listing.LivingArea),
		formatInteger(listing.DaysOnZillow),
		listing.Availability,
	)
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
	if columns.History {
		row = append(row, listing.HistoryStatus, strings.Join(listing.HistoryChanges, ","))
	}
	if columns.Verification {
		row = append(row, strings.Join(listing.VerificationNotes, ","))
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
		if listing.RequiredMonthlyFees != nil || listing.TotalMonthlyCost != nil || listing.PriceIncludesRequiredFees != nil {
			columns.Cost = true
		}
		if listing.MatchStatus != "" {
			columns.Match = true
		}
		if listing.HistoryStatus != "" || len(listing.HistoryChanges) > 0 {
			columns.History = true
		}
		if len(listing.VerificationNotes) > 0 {
			columns.Verification = true
		}
	}
	return columns
}

func mergeListingTableColumns(left, right listingTableColumns) listingTableColumns {
	return listingTableColumns{
		Details:      left.Details || right.Details,
		Cost:         left.Cost || right.Cost,
		Match:        left.Match || right.Match,
		History:      left.History || right.History,
		Verification: left.Verification || right.Verification,
	}
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
