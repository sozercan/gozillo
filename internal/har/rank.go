package har

import (
	"fmt"
	"net/url"
	"path"
	"sort"
	"strings"
)

const searchStatePath = "/async-create-search-page-state"

// Candidate is one ranked HAR entry. EntryIndex refers to Log.Entries and is
// stable even after candidates are sorted by score.
type Candidate struct {
	EntryIndex   int      `json:"entryIndex"`
	Score        int      `json:"score"`
	Method       string   `json:"method"`
	URL          string   `json:"url"`
	Host         string   `json:"host,omitempty"`
	Path         string   `json:"path,omitempty"`
	ResourceType string   `json:"resourceType,omitempty"`
	RequestType  string   `json:"requestMimeType,omitempty"`
	ResponseType string   `json:"responseMimeType,omitempty"`
	Reasons      []string `json:"reasons"`
}

// RankCandidates scores every entry and returns highest-scoring candidates
// first. Ties are resolved by original HAR entry order.
func RankCandidates(archive *Archive) ([]Candidate, error) {
	if err := archive.Validate(); err != nil {
		return nil, fmt.Errorf("rank HAR candidates: %w", err)
	}

	candidates := make([]Candidate, 0, len(archive.Log.Entries))
	for index := range archive.Log.Entries {
		candidates = append(candidates, scoreCandidate(index, &archive.Log.Entries[index]))
	}
	sort.SliceStable(candidates, func(left, right int) bool {
		if candidates[left].Score != candidates[right].Score {
			return candidates[left].Score > candidates[right].Score
		}
		return candidates[left].EntryIndex < candidates[right].EntryIndex
	})
	return candidates, nil
}

func scoreCandidate(index int, entry *Entry) Candidate {
	candidate := Candidate{
		EntryIndex:   index,
		Method:       strings.ToUpper(entry.Request.Method),
		ResourceType: strings.ToLower(entry.ResourceType),
		RequestType:  requestMediaType(&entry.Request),
		ResponseType: responseMediaType(&entry.Response),
	}

	safeURL, sanitizeErr := sanitizeURL(entry.Request.URL, DefaultRedaction, sensitiveKeyNames)
	if sanitizeErr == nil {
		candidate.URL = safeURL
	}

	parsed, err := url.Parse(safeURL)
	if err != nil || sanitizeErr != nil || safeURL == "" {
		candidate.Score -= 30
		candidate.Reasons = append(candidate.Reasons, "-30 malformed URL")
	} else {
		candidate.Host = strings.ToLower(parsed.Hostname())
		candidate.Path = parsed.EscapedPath()
		if candidate.Path == "" {
			candidate.Path = "/"
		}

		firstParty := isFirstPartyZillowHost(parsed.Hostname())
		if firstParty {
			candidate.Score += 40
			candidate.Reasons = append(candidate.Reasons, "+40 first-party Zillow host")
		} else {
			candidate.Score -= 60
			candidate.Reasons = append(candidate.Reasons, "-60 non-Zillow host")
		}

		if firstParty && normalizedSearchPath(parsed.Path) == searchStatePath {
			candidate.Score += 140
			candidate.Reasons = append(candidate.Reasons, "+140 search-state endpoint")
		}

		if isTelemetryRequest(parsed.Hostname(), parsed.Path) {
			candidate.Score -= 120
			candidate.Reasons = append(candidate.Reasons, "-120 analytics/telemetry")
		}
		if isStaticRequest(candidate.ResourceType, parsed.Path) {
			candidate.Score -= 90
			candidate.Reasons = append(candidate.Reasons, "-90 static asset")
		}
	}

	switch candidate.ResourceType {
	case "fetch", "xhr", "xmlhttprequest":
		candidate.Score += 35
		candidate.Reasons = append(candidate.Reasons, "+35 fetch/XHR")
	case "beacon", "ping":
		candidate.Score -= 80
		candidate.Reasons = append(candidate.Reasons, "-80 beacon/ping")
	}

	if isJSONMediaType(candidate.ResponseType) {
		candidate.Score += 30
		candidate.Reasons = append(candidate.Reasons, "+30 JSON response")
	}
	if isJSONMediaType(candidate.RequestType) {
		candidate.Score += 15
		candidate.Reasons = append(candidate.Reasons, "+15 JSON request")
	}

	switch candidate.Method {
	case "PUT":
		candidate.Score += 12
		candidate.Reasons = append(candidate.Reasons, "+12 PUT")
	case "GET":
		candidate.Score += 4
		candidate.Reasons = append(candidate.Reasons, "+4 GET")
	}

	if entry.Response.Status >= 200 && entry.Response.Status < 300 {
		candidate.Score += 5
		candidate.Reasons = append(candidate.Reasons, "+5 successful response")
	} else if entry.Response.Status >= 400 {
		candidate.Score -= 25
		candidate.Reasons = append(candidate.Reasons, "-25 error response")
	}

	return candidate
}

func requestMediaType(request *Request) string {
	if request.PostData != nil && request.PostData.MimeType != "" {
		return request.PostData.MimeType
	}
	return headerValue(request.Headers, "Content-Type")
}

func responseMediaType(response *Response) string {
	if response.Content.MimeType != "" {
		return response.Content.MimeType
	}
	return headerValue(response.Headers, "Content-Type")
}

func headerValue(headers []NameValue, name string) string {
	for _, header := range headers {
		if strings.EqualFold(header.Name, name) {
			return header.Value
		}
	}
	return ""
}

func isFirstPartyZillowHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	return host == "zillow.com" || strings.HasSuffix(host, ".zillow.com")
}

func normalizedSearchPath(value string) string {
	if value != "/" {
		value = strings.TrimSuffix(value, "/")
	}
	return value
}

func isTelemetryRequest(host, requestPath string) bool {
	value := strings.ToLower(host + " " + requestPath)
	for _, marker := range []string{
		"analytics", "telemetry", "doubleclick", "google-analytics", "newrelic",
		"segment", "mixpanel", "scorecardresearch", "/beacon", "/collect",
		"/event", "/events", "/logging", "/metrics", "/pixel", "/track",
	} {
		if strings.Contains(value, marker) {
			return true
		}
	}
	return false
}

func isStaticRequest(resourceType, requestPath string) bool {
	switch resourceType {
	case "document", "font", "image", "media", "script", "stylesheet":
		return true
	}

	switch strings.ToLower(path.Ext(requestPath)) {
	case ".avif", ".css", ".gif", ".ico", ".jpeg", ".jpg", ".js", ".map", ".mp4", ".png", ".svg", ".webp", ".woff", ".woff2":
		return true
	}
	return false
}
