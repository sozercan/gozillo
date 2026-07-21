package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"gozillo/internal/zillow"
)

const (
	filterAny             = "any"
	unknownLaundryExclude = "exclude"
	unknownLaundryWatch   = "watchlist"
	availabilityExclude   = "exclude"
	availabilityWatchlist = "watchlist"
)

type detailFilterOptions struct {
	Enrich                  bool
	VerifyRecency           bool
	Workers                 int
	Delay                   time.Duration
	MinSqft                 int64
	MaxDaysOnZillow         int64
	AvailableFrom           *time.Time
	AvailableBy             *time.Time
	UnknownAvailability     string
	OutOfWindowAvailability string
	MaxTotalCost            int64
	RequireForRent          bool
	ExcludeSharedHousing    bool
	ExcludeStudentHousing   bool
	ExcludeIncomeRestricted bool
	Laundry                 string
	Parking                 string
	Pets                    string
	Flex                    []string
	UnknownLaundry          string
}

func (options detailFilterOptions) needsEnrichment() bool {
	return options.Enrich || options.VerifyRecency || options.requiresAmenityDetails()
}

func (options detailFilterOptions) requiresAmenityDetails() bool {
	return options.Laundry != filterAny || options.Parking != filterAny || options.Pets != filterAny || len(options.Flex) > 0 || options.RequireForRent || options.ExcludeSharedHousing || options.ExcludeStudentHousing || options.ExcludeIncomeRestricted
}

func (options detailFilterOptions) hasAdvancedFilters() bool {
	return options.MinSqft > 0 || options.MaxDaysOnZillow >= 0 || options.AvailableFrom != nil || options.AvailableBy != nil || options.MaxTotalCost > 0 || options.RequireForRent || options.ExcludeSharedHousing || options.ExcludeStudentHousing || options.ExcludeIncomeRestricted ||
		options.Laundry != filterAny || options.Parking != filterAny || options.Pets != filterAny || len(options.Flex) > 0
}

func validateDetailFilterOptions(options detailFilterOptions) error {
	if options.Workers < 1 || options.Workers > 8 {
		return errors.New("detail-workers must be between 1 and 8")
	}
	if options.Delay < 0 {
		return errors.New("detail-delay must not be negative")
	}
	if options.MinSqft < 0 {
		return errors.New("min-sqft must not be negative")
	}
	if options.MaxDaysOnZillow < -1 {
		return errors.New("max-days-on-zillow must be -1 or non-negative")
	}
	if options.AvailableFrom != nil && options.AvailableBy != nil && options.AvailableFrom.After(*options.AvailableBy) {
		return errors.New("available-from must not be after available-by")
	}
	if options.MaxTotalCost < 0 {
		return errors.New("max-total-cost must not be negative")
	}
	if !oneOf(options.UnknownAvailability, "", availabilityExclude, availabilityWatchlist) {
		return fmt.Errorf("unknown-availability value %q is invalid (want exclude or watchlist)", options.UnknownAvailability)
	}
	if !oneOf(options.OutOfWindowAvailability, "", availabilityExclude, availabilityWatchlist) {
		return fmt.Errorf("out-of-window-availability value %q is invalid (want exclude or watchlist)", options.OutOfWindowAvailability)
	}
	if !oneOf(options.Laundry, filterAny, zillow.LaundryInUnit, zillow.LaundryHookups, zillow.LaundryShared, zillow.LaundryNone, zillow.LaundryUnknown) {
		return fmt.Errorf("unknown laundry value %q (want any, in-unit, hookups, shared, none, or unknown)", options.Laundry)
	}
	if !oneOf(options.Parking, filterAny, zillow.ParkingAvailable, zillow.ParkingGarage, zillow.ParkingPrivateGarage, zillow.ParkingNone, zillow.ParkingUnknown) {
		return fmt.Errorf("unknown parking value %q (want any, available, garage, private-garage, none, or unknown)", options.Parking)
	}
	if !oneOf(options.Pets, filterAny, "allowed", "dogs", "cats", "none", zillow.PetPolicyUnknown) {
		return fmt.Errorf("unknown pets value %q (want any, allowed, dogs, cats, none, or unknown)", options.Pets)
	}
	if !oneOf(options.UnknownLaundry, unknownLaundryExclude, unknownLaundryWatch) {
		return fmt.Errorf("unknown-laundry value %q is invalid (want exclude or watchlist)", options.UnknownLaundry)
	}
	if options.UnknownLaundry == unknownLaundryWatch && options.Laundry == filterAny {
		return errors.New("unknown-laundry=watchlist requires a --laundry filter")
	}
	for _, value := range options.Flex {
		if !oneOf(value, "den", "office", "bonus", "loft", "flex", zillow.ParkingPrivateGarage) {
			return fmt.Errorf("unknown flex value %q (want den, office, bonus, loft, flex, or private-garage)", value)
		}
	}
	return nil
}

func normalizeDetailChoice(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func parseDateFlag(value string) (*time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil {
		return nil, errors.New("must use YYYY-MM-DD")
	}
	return &parsed, nil
}

func parseFlexValues(values []string) ([]string, error) {
	result := make([]string, 0)
	seen := make(map[string]struct{})
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			part = normalizeDetailChoice(part)
			if part == "" {
				return nil, errors.New("contains an empty comma-separated value")
			}
			if _, exists := seen[part]; exists {
				continue
			}
			seen[part] = struct{}{}
			result = append(result, part)
		}
	}
	return result, nil
}

func oneOf(value string, choices ...string) bool {
	for _, choice := range choices {
		if value == choice {
			return true
		}
	}
	return false
}

type detailFetchResult struct {
	page *zillow.RentalPage
	err  error
}

type propertyDiskRecord struct {
	Page       *zillow.RentalPage `json:"page,omitempty"`
	Property   *zillow.Property   `json:"property,omitempty"` // legacy cache compatibility
	Error      string             `json:"error,omitempty"`
	RetryAfter time.Time          `json:"retryAfter,omitempty"`
}

type propertyFetcher interface {
	FetchRentalPage(context.Context, string) (*zillow.RentalPage, error)
}

type listingDetailEnricher struct {
	client    propertyFetcher
	workers   int
	delay     time.Duration
	paceMu    sync.Mutex
	nextStart time.Time
	mu        sync.Mutex
	cache     map[string]detailFetchResult
	diskCache *diskCache
}

func newListingDetailEnricher(client propertyFetcher, workers int, delay time.Duration, persistent *diskCache) *listingDetailEnricher {
	return &listingDetailEnricher{
		client:    client,
		workers:   workers,
		delay:     delay,
		cache:     make(map[string]detailFetchResult),
		diskCache: persistent,
	}
}

func (enricher *listingDetailEnricher) Enrich(ctx context.Context, listings []zillow.Listing) []zillow.Listing {
	if enricher == nil || enricher.client == nil || len(listings) == 0 {
		return listings
	}

	uniqueURLs := make([]string, 0, len(listings))
	seen := make(map[string]struct{}, len(listings))
	for _, listing := range listings {
		url := strings.TrimSpace(listing.URL)
		if url == "" {
			continue
		}
		if _, exists := seen[url]; exists {
			continue
		}
		seen[url] = struct{}{}
		if _, cached := enricher.cached(url); cached {
			continue
		}
		if result, hit := enricher.loadPersistent(url); hit {
			enricher.store(url, result)
			continue
		}
		uniqueURLs = append(uniqueURLs, url)
	}

	jobs := make(chan string)
	var workers sync.WaitGroup
	workerCount := enricher.workers
	if workerCount < 1 {
		workerCount = 1
	}
	if workerCount > len(uniqueURLs) {
		workerCount = len(uniqueURLs)
	}
	for range workerCount {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for url := range jobs {
				if err := enricher.waitForTurn(ctx); err != nil {
					enricher.store(url, detailFetchResult{err: err})
					continue
				}
				page, err := enricher.client.FetchRentalPage(ctx, url)
				result := detailFetchResult{page: page, err: err}
				enricher.savePersistent(url, result)
				enricher.store(url, result)
			}
		}()
	}
	for _, url := range uniqueURLs {
		jobs <- url
	}
	close(jobs)
	workers.Wait()

	expanded := make([]zillow.Listing, 0, len(listings))
	for index := range listings {
		url := strings.TrimSpace(listings[index].URL)
		if url == "" {
			markDetailsUnavailable(&listings[index], "listing has no property URL")
			expanded = append(expanded, listings[index])
			continue
		}
		result, ok := enricher.cached(url)
		if !ok {
			markDetailsUnavailable(&listings[index], "property details were not fetched")
			expanded = append(expanded, listings[index])
			continue
		}
		if result.err != nil {
			markDetailsUnavailable(&listings[index], result.err.Error())
			expanded = append(expanded, listings[index])
			continue
		}
		if result.page == nil || len(result.page.Properties) == 0 {
			markDetailsUnavailable(&listings[index], "rental page returned no details")
			expanded = append(expanded, listings[index])
			continue
		}
		for propertyIndex := range result.page.Properties {
			if propertyIndex == 0 {
				if result.page.Kind == zillow.RentalPageCommunity {
					mergeCommunityPropertyDetails(&listings[index], &result.page.Properties[propertyIndex])
				} else {
					mergePropertyDetails(&listings[index], &result.page.Properties[propertyIndex])
				}
				expanded = append(expanded, listings[index])
				continue
			}
			candidate := listings[index]
			mergeCommunityPropertyDetails(&candidate, &result.page.Properties[propertyIndex])
			expanded = append(expanded, candidate)
		}
	}
	return expanded
}

func (enricher *listingDetailEnricher) loadPersistent(url string) (detailFetchResult, bool) {
	if enricher == nil || enricher.diskCache == nil {
		return detailFetchResult{}, false
	}
	var record propertyDiskRecord
	hit, err := enricher.diskCache.Load(url, &record)
	if err != nil || !hit {
		return detailFetchResult{}, false
	}
	if record.Page != nil {
		return detailFetchResult{page: record.Page}, true
	}
	if record.Property != nil {
		return detailFetchResult{page: &zillow.RentalPage{Kind: zillow.RentalPageProperty, Properties: []zillow.Property{*record.Property}}}, true
	}
	if record.Error != "" && record.RetryAfter.After(enricher.diskCache.now()) {
		return detailFetchResult{err: errors.New(record.Error)}, true
	}
	return detailFetchResult{}, false
}

func (enricher *listingDetailEnricher) savePersistent(url string, result detailFetchResult) {
	if enricher == nil || enricher.diskCache == nil {
		return
	}
	record := propertyDiskRecord{Page: result.page}
	if result.err != nil {
		switch {
		case errors.Is(result.err, zillow.ErrSchemaDrift):
			record.Error = result.err.Error()
			record.RetryAfter = enricher.diskCache.now().Add(enricher.diskCache.TTL)
		case retryableZillowError(result.err):
			record.Error = result.err.Error()
			record.RetryAfter = enricher.diskCache.now().Add(5 * time.Minute)
		default:
			return
		}
	}
	_ = enricher.diskCache.Save(url, record)
}

func (enricher *listingDetailEnricher) waitForTurn(ctx context.Context) error {
	if enricher.delay <= 0 {
		return nil
	}
	enricher.paceMu.Lock()
	now := time.Now()
	start := now
	if enricher.nextStart.After(start) {
		start = enricher.nextStart
	}
	enricher.nextStart = start.Add(enricher.delay)
	enricher.paceMu.Unlock()
	return sleepContext(ctx, time.Until(start))
}

func (enricher *listingDetailEnricher) cached(url string) (detailFetchResult, bool) {
	enricher.mu.Lock()
	defer enricher.mu.Unlock()
	result, ok := enricher.cache[url]
	return result, ok
}

func (enricher *listingDetailEnricher) store(url string, result detailFetchResult) {
	enricher.mu.Lock()
	defer enricher.mu.Unlock()
	enricher.cache[url] = result
}

func markDetailsUnavailable(listing *zillow.Listing, detail string) {
	if listing == nil {
		return
	}
	listing.DetailStatus = zillow.DetailStatusUnavailable
	listing.DetailError = detail
	listing.Laundry = zillow.LaundryUnknown
	listing.Parking = zillow.ParkingUnknown
	listing.PetPolicy = zillow.PetPolicyUnknown
}

func mergePropertyDetails(listing *zillow.Listing, property *zillow.Property) {
	if listing == nil || property == nil {
		return
	}
	listing.DetailStatus = zillow.DetailStatusEnriched
	listing.DetailError = ""
	listing.YearBuilt = property.YearBuilt
	listing.Description = property.Description
	listing.Laundry = property.Laundry
	listing.LaundryFeatures = append([]string(nil), property.LaundryFeatures...)
	listing.Parking = property.Parking
	listing.ParkingFeatures = append([]string(nil), property.ParkingFeatures...)
	listing.PetPolicy = property.PetPolicy
	listing.AllowedPets = append([]string(nil), property.AllowedPets...)
	listing.FlexSpaces = append([]string(nil), property.FlexSpaces...)
	listing.RequiredMonthlyFees = property.RequiredMonthlyFees
	listing.TotalMonthlyCost = property.TotalMonthlyCost
	listing.PriceIncludesRequiredFees = property.PriceIncludesRequiredFees
	listing.VerificationNotes = append([]string(nil), property.VerificationNotes...)
	listing.SharedHousing = property.SharedHousing
	listing.StudentHousing = property.StudentHousing
	listing.IncomeRestricted = property.IncomeRestricted
	if property.Price != nil {
		listing.Price = property.Price
	}
	if property.Bedrooms != nil {
		listing.Bedrooms = property.Bedrooms
	}
	if property.Bathrooms != nil {
		listing.Bathrooms = property.Bathrooms
	}
	if property.LivingArea != nil {
		listing.LivingArea = property.LivingArea
	}
	if property.DaysOnZillow != nil {
		listing.DaysOnZillow = property.DaysOnZillow
	}
	if property.ListedDate != "" {
		listing.ListedDate = property.ListedDate
	}
	if property.UpdatedDate != "" {
		listing.UpdatedDate = property.UpdatedDate
	}
	if strings.TrimSpace(property.Availability) != "" {
		listing.Availability = property.Availability
	}
	if property.HomeType != "" {
		listing.HomeType = property.HomeType
	}
	if property.Status != "" {
		listing.Status = property.Status
	}
}

func mergeCommunityPropertyDetails(listing *zillow.Listing, property *zillow.Property) {
	if listing == nil || property == nil {
		return
	}
	originalDays := listing.DaysOnZillow
	mergePropertyDetails(listing, property)
	if property.ID != "" {
		listing.ID = property.ID
	}
	if property.URL != "" {
		listing.URL = property.URL
	}
	if property.Address.Full != "" {
		listing.Address = property.Address
	}
	if property.Price != nil {
		listing.Price = property.Price
	}
	if property.Bedrooms != nil {
		listing.Bedrooms = property.Bedrooms
	}
	if property.Bathrooms != nil {
		listing.Bathrooms = property.Bathrooms
	}
	if property.LivingArea != nil {
		listing.LivingArea = property.LivingArea
	}
	if property.Status != "" {
		listing.Status = property.Status
	}
	if property.HomeType != "" {
		listing.HomeType = property.HomeType
	}
	if property.Availability != "" {
		listing.Availability = property.Availability
	}
	if listing.DaysOnZillow == nil {
		listing.DaysOnZillow = originalDays
	}
}

func prefilterKnownListingMetadata(listings []zillow.Listing, options detailFilterOptions, today time.Time) []zillow.Listing {
	filtered := make([]zillow.Listing, 0, len(listings))
	for _, listing := range listings {
		if options.MinSqft > 0 && listing.LivingArea != nil && *listing.LivingArea < options.MinSqft {
			continue
		}
		if options.MaxDaysOnZillow >= 0 && listing.DaysOnZillow != nil && *listing.DaysOnZillow > options.MaxDaysOnZillow {
			continue
		}
		if options.AvailableFrom != nil || options.AvailableBy != nil {
			if available, ok := listingAvailabilityDate(listing.Availability, today); ok && options.OutOfWindowAvailability == availabilityExclude {
				if options.AvailableFrom != nil && available.Before(*options.AvailableFrom) {
					continue
				}
				if options.AvailableBy != nil && available.After(*options.AvailableBy) {
					continue
				}
			}
		}
		if options.MaxTotalCost > 0 && listing.TotalMonthlyCost != nil && *listing.TotalMonthlyCost > options.MaxTotalCost {
			continue
		}
		if options.RequireForRent && strings.TrimSpace(listing.Status) != "" && !isForRentStatus(listing.Status) {
			continue
		}
		if options.ExcludeSharedHousing && listing.SharedHousing {
			continue
		}
		if options.ExcludeStudentHousing && listing.StudentHousing {
			continue
		}
		if options.ExcludeIncomeRestricted && listing.IncomeRestricted {
			continue
		}
		filtered = append(filtered, listing)
	}
	return filtered
}

func filterDetailedListings(listings []zillow.Listing, options detailFilterOptions, today time.Time) []zillow.Listing {
	if !options.hasAdvancedFilters() {
		return listings
	}

	filtered := make([]zillow.Listing, 0, len(listings))
	for _, listing := range listings {
		listing.MatchStatus = zillow.MatchStatusMatch
		listing.MatchReasons = nil

		if options.ExcludeSharedHousing && listing.SharedHousing {
			continue
		}
		if options.ExcludeStudentHousing && listing.StudentHousing {
			continue
		}
		if options.ExcludeIncomeRestricted && listing.IncomeRestricted {
			continue
		}
		if options.MinSqft > 0 && (listing.LivingArea == nil || *listing.LivingArea < options.MinSqft) {
			continue
		}
		if options.MaxDaysOnZillow >= 0 && (listing.DaysOnZillow == nil || *listing.DaysOnZillow > options.MaxDaysOnZillow) {
			continue
		}
		if options.AvailableFrom != nil || options.AvailableBy != nil {
			available, ok := listingAvailabilityDate(listing.Availability, today)
			if !ok {
				if options.UnknownAvailability == availabilityWatchlist {
					markListingWatchlist(&listing, "availability needs verification")
				} else {
					continue
				}
			} else {
				outside := options.AvailableFrom != nil && available.Before(*options.AvailableFrom) || options.AvailableBy != nil && available.After(*options.AvailableBy)
				if outside {
					if options.OutOfWindowAvailability == availabilityWatchlist {
						markListingWatchlist(&listing, "availability is outside the target window")
					} else {
						continue
					}
				}
			}
		}
		if options.MaxTotalCost > 0 {
			if listing.TotalMonthlyCost == nil {
				markListingWatchlist(&listing, "total monthly cost needs verification")
			} else if *listing.TotalMonthlyCost > options.MaxTotalCost {
				continue
			}
		}
		if options.RequireForRent {
			if strings.TrimSpace(listing.Status) == "" {
				markListingWatchlist(&listing, "current rental status needs verification")
			} else if !isForRentStatus(listing.Status) {
				continue
			}
		}

		laundry := normalizeDetailChoice(listing.Laundry)
		if laundry == "" {
			laundry = zillow.LaundryUnknown
		}
		if options.Laundry != filterAny && laundry != options.Laundry {
			if laundry == zillow.LaundryUnknown && options.UnknownLaundry == unknownLaundryWatch {
				listing.MatchStatus = zillow.MatchStatusWatchlist
				listing.MatchReasons = append(listing.MatchReasons, "laundry details are unknown")
			} else {
				continue
			}
		}
		if !parkingMatches(options.Parking, listing.Parking) {
			continue
		}
		if !petsMatch(options.Pets, listing.PetPolicy, listing.AllowedPets) {
			continue
		}
		if len(options.Flex) > 0 && !hasAnyValue(listing.FlexSpaces, options.Flex) {
			continue
		}
		if isTwoBedPrivateGarageOnly(listing) {
			markListingWatchlist(&listing, "private garage flex requires exclusive-use and inclusion verification")
		}
		filtered = append(filtered, listing)
	}
	return filtered
}

func markListingWatchlist(listing *zillow.Listing, reason string) {
	if listing == nil {
		return
	}
	listing.MatchStatus = zillow.MatchStatusWatchlist
	for _, existing := range listing.MatchReasons {
		if existing == reason {
			return
		}
	}
	listing.MatchReasons = append(listing.MatchReasons, reason)
}

func isForRentStatus(value string) bool {
	normalized := strings.ToUpper(strings.TrimSpace(value))
	return normalized == "FOR_RENT" || normalized == "FOR RENT" || strings.HasPrefix(normalized, "FOR_RENT_")
}

func isTwoBedPrivateGarageOnly(listing zillow.Listing) bool {
	if listing.Bedrooms == nil || *listing.Bedrooms != 2 {
		return false
	}
	hasGarage := false
	for _, value := range listing.FlexSpaces {
		normalized := normalizeDetailChoice(value)
		if normalized == zillow.ParkingPrivateGarage {
			hasGarage = true
			continue
		}
		if oneOf(normalized, "den", "office", "bonus", "loft", "flex") {
			return false
		}
	}
	return hasGarage
}

func parkingMatches(filter, value string) bool {
	if filter == filterAny {
		return true
	}
	value = normalizeDetailChoice(value)
	if value == "" {
		value = zillow.ParkingUnknown
	}
	switch filter {
	case zillow.ParkingAvailable:
		return value == zillow.ParkingAvailable || value == zillow.ParkingGarage || value == zillow.ParkingPrivateGarage
	case zillow.ParkingGarage:
		return value == zillow.ParkingGarage || value == zillow.ParkingPrivateGarage
	default:
		return value == filter
	}
}

func petsMatch(filter, policy string, allowedPets []string) bool {
	if filter == filterAny {
		return true
	}
	policy = normalizeDetailChoice(policy)
	if policy == "" {
		policy = zillow.PetPolicyUnknown
	}
	switch filter {
	case "allowed":
		return policy == zillow.PetPolicyAllowed || policy == zillow.PetPolicyRestricted
	case "none":
		return policy == zillow.PetPolicyNotAllowed
	case zillow.PetPolicyUnknown:
		return policy == zillow.PetPolicyUnknown
	case "dogs", "cats":
		if policy != zillow.PetPolicyAllowed && policy != zillow.PetPolicyRestricted {
			return false
		}
		text := strings.ToLower(strings.Join(allowedPets, " "))
		return strings.Contains(text, strings.TrimSuffix(filter, "s")) || strings.Contains(text, "pets allowed") || strings.Contains(text, "all pets")
	default:
		return false
	}
}

func hasAnyValue(values, wanted []string) bool {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[normalizeDetailChoice(value)] = struct{}{}
	}
	for _, value := range wanted {
		if _, ok := set[normalizeDetailChoice(value)]; ok {
			return true
		}
	}
	return false
}

func listingAvailabilityDate(value string, today time.Time) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	lower := strings.ToLower(value)
	if lower == "available now" || lower == "now" || lower == "immediately" || lower == "immediate" {
		return dateOnly(today), true
	}
	for _, layout := range []string{"2006-01-02", "2006-01-02 15:04:05", time.RFC3339, "Jan 2, 2006", "January 2, 2006"} {
		if parsed, err := time.Parse(layout, value); err == nil {
			return dateOnly(parsed), true
		}
	}
	return time.Time{}, false
}

func dateOnly(value time.Time) time.Time {
	year, month, day := value.Date()
	return time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
}

// EnrichRecency performs a second, non-destructive pass over expanded unit
// URLs. It fills only recency fields, leaving already-confirmed rental facts
// intact when a unit page is unavailable or challenged.
func (enricher *listingDetailEnricher) EnrichRecency(ctx context.Context, listings []zillow.Listing) []zillow.Listing {
	if enricher == nil || enricher.client == nil || len(listings) == 0 {
		return listings
	}
	seen := make(map[string]struct{})
	for _, listing := range listings {
		if listing.DaysOnZillow != nil || strings.TrimSpace(listing.URL) == "" {
			continue
		}
		url := strings.TrimSpace(listing.URL)
		if _, exists := seen[url]; exists {
			continue
		}
		seen[url] = struct{}{}
		if _, cached := enricher.cached(url); cached {
			continue
		}
		if result, hit := enricher.loadPersistent(url); hit {
			enricher.store(url, result)
			continue
		}
		if err := enricher.waitForTurn(ctx); err != nil {
			enricher.store(url, detailFetchResult{err: err})
			continue
		}
		page, err := enricher.client.FetchRentalPage(ctx, url)
		result := detailFetchResult{page: page, err: err}
		enricher.savePersistent(url, result)
		enricher.store(url, result)
	}

	for index := range listings {
		if listings[index].DaysOnZillow != nil {
			continue
		}
		result, ok := enricher.cached(strings.TrimSpace(listings[index].URL))
		if !ok || result.err != nil || result.page == nil {
			continue
		}
		property := rentalPagePropertyForListing(result.page, listings[index])
		if property == nil {
			continue
		}
		if property.DaysOnZillow != nil {
			listings[index].DaysOnZillow = property.DaysOnZillow
		}
		if property.ListedDate != "" {
			listings[index].ListedDate = property.ListedDate
		}
		if property.UpdatedDate != "" {
			listings[index].UpdatedDate = property.UpdatedDate
		}
	}
	return listings
}

func rentalPagePropertyForListing(page *zillow.RentalPage, listing zillow.Listing) *zillow.Property {
	if page == nil {
		return nil
	}
	for index := range page.Properties {
		if listing.ID != "" && page.Properties[index].ID == listing.ID {
			return &page.Properties[index]
		}
		if listing.URL != "" && page.Properties[index].URL == listing.URL {
			return &page.Properties[index]
		}
	}
	if len(page.Properties) == 1 {
		return &page.Properties[0]
	}
	return nil
}
