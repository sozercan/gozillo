package zillow

import "encoding/json"

// Address is the normalized postal address shared by listings and property pages.
type Address struct {
	Full       string `json:"full,omitempty"`
	Street     string `json:"street,omitempty"`
	City       string `json:"city,omitempty"`
	State      string `json:"state,omitempty"`
	PostalCode string `json:"postalCode,omitempty"`
}

// Coordinates is a normalized latitude/longitude pair.
type Coordinates struct {
	Latitude  *float64 `json:"latitude,omitempty"`
	Longitude *float64 `json:"longitude,omitempty"`
}

// Listing is the stable, normalized representation of a Zillow search result.
type Listing struct {
	ID              string      `json:"id,omitempty"`
	URL             string      `json:"url,omitempty"`
	Address         Address     `json:"address,omitempty"`
	Price           *int64      `json:"price,omitempty"`
	PriceText       string      `json:"priceText,omitempty"`
	Bedrooms        *float64    `json:"bedrooms,omitempty"`
	Bathrooms       *float64    `json:"bathrooms,omitempty"`
	LivingArea      *int64      `json:"livingArea,omitempty"`
	HomeType        string      `json:"homeType,omitempty"`
	Status          string      `json:"status,omitempty"`
	ImageURL        string      `json:"imageUrl,omitempty"`
	Coordinates     Coordinates `json:"coordinates,omitempty"`
	DaysOnZillow    *int64      `json:"daysOnZillow,omitempty"`
	Availability    string      `json:"availabilityDate,omitempty"`
	YearBuilt       *int64      `json:"yearBuilt,omitempty"`
	Description     string      `json:"description,omitempty"`
	Laundry         string      `json:"laundry,omitempty"`
	LaundryFeatures []string    `json:"laundryFeatures,omitempty"`
	Parking         string      `json:"parking,omitempty"`
	ParkingFeatures []string    `json:"parkingFeatures,omitempty"`
	PetPolicy       string      `json:"petPolicy,omitempty"`
	AllowedPets     []string    `json:"allowedPets,omitempty"`
	FlexSpaces      []string    `json:"flexSpaces,omitempty"`
	DetailStatus    string      `json:"detailStatus,omitempty"`
	DetailError     string      `json:"detailError,omitempty"`
	MatchStatus     string      `json:"matchStatus,omitempty"`
	MatchReasons    []string    `json:"matchReasons,omitempty"`
}

// SearchMetadata summarizes a search response independently of Zillow's wire format.
type SearchMetadata struct {
	RequestID      uint64 `json:"requestId"`
	CurrentPage    int    `json:"currentPage,omitempty"`
	Returned       int    `json:"returned"`
	TotalResults   int    `json:"totalResults,omitempty"`
	ResultsHash    string `json:"resultsHash,omitempty"`
	RelaxedResults bool   `json:"relaxedResults,omitempty"`
}

// SearchResult contains normalized listings and optional raw response JSON.
type SearchResult struct {
	Listings []Listing       `json:"listings"`
	Metadata SearchMetadata  `json:"metadata"`
	Raw      json.RawMessage `json:"raw,omitempty"`
}

// Property is the normalized representation of a Zillow property page.
type Property struct {
	ID              string          `json:"id,omitempty"`
	URL             string          `json:"url,omitempty"`
	Address         Address         `json:"address,omitempty"`
	Price           *int64          `json:"price,omitempty"`
	Bedrooms        *float64        `json:"bedrooms,omitempty"`
	Bathrooms       *float64        `json:"bathrooms,omitempty"`
	LivingArea      *int64          `json:"livingArea,omitempty"`
	LotSize         *float64        `json:"lotSize,omitempty"`
	YearBuilt       *int64          `json:"yearBuilt,omitempty"`
	HomeType        string          `json:"homeType,omitempty"`
	Status          string          `json:"status,omitempty"`
	Description     string          `json:"description,omitempty"`
	ImageURL        string          `json:"imageUrl,omitempty"`
	Coordinates     Coordinates     `json:"coordinates,omitempty"`
	DaysOnZillow    *int64          `json:"daysOnZillow,omitempty"`
	Availability    string          `json:"availabilityDate,omitempty"`
	Laundry         string          `json:"laundry,omitempty"`
	LaundryFeatures []string        `json:"laundryFeatures,omitempty"`
	Parking         string          `json:"parking,omitempty"`
	ParkingFeatures []string        `json:"parkingFeatures,omitempty"`
	PetPolicy       string          `json:"petPolicy,omitempty"`
	AllowedPets     []string        `json:"allowedPets,omitempty"`
	FlexSpaces      []string        `json:"flexSpaces,omitempty"`
	Raw             json.RawMessage `json:"raw,omitempty"`
}
