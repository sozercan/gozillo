package zillow

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// SearchSnapshotOptions controls search snapshot ingestion from a browser capture.
type SearchSnapshotOptions struct {
	IncludeRaw bool
	MaxBytes   int64
}

// SearchSnapshotReaderOptions is an alias kept for symmetry with PropertyReaderOptions.
type SearchSnapshotReaderOptions = SearchSnapshotOptions

// ReadSearchSnapshot parses saved Zillow HTML or raw __NEXT_DATA__ JSON.
func ReadSearchSnapshot(reader io.Reader) (*SearchResult, error) {
	return ReadSearchSnapshotWithOptions(reader, SearchSnapshotOptions{})
}

// ParseSearchSnapshot is an alias for ReadSearchSnapshot.
func ParseSearchSnapshot(reader io.Reader) (*SearchResult, error) {
	return ReadSearchSnapshot(reader)
}

// ReadSearchSnapshotWithOptions parses a bounded browser search snapshot.
func ReadSearchSnapshotWithOptions(reader io.Reader, options SearchSnapshotOptions) (*SearchResult, error) {
	if reader == nil {
		return nil, errors.New("read Zillow search snapshot: reader is nil")
	}

	limit := options.MaxBytes
	if limit == 0 {
		limit = DefaultMaxResponseBytes
	}
	if limit < 0 {
		return nil, errors.New("read Zillow search snapshot: maximum size must be positive")
	}

	document, err := readBounded(reader, limit)
	if err != nil {
		if errors.Is(err, ErrResponseTooLarge) {
			return nil, &ResponseTooLargeError{Limit: limit}
		}
		return nil, fmt.Errorf("read Zillow search snapshot: %w", err)
	}

	trimmed := bytes.TrimSpace(bytes.TrimPrefix(document, []byte{0xef, 0xbb, 0xbf}))
	if len(trimmed) == 0 {
		return nil, searchSnapshotSchemaError("input", "snapshot is empty")
	}

	nextData := trimmed
	htmlSnapshot := looksLikeSearchSnapshotHTML(trimmed)
	if htmlSnapshot {
		var ok bool
		nextData, ok = extractNextData(document)
		if !ok {
			if reason := searchSnapshotChallengeReason(document); reason != "" {
				return nil, &ChallengeError{StatusCode: http.StatusOK, Reason: reason}
			}
			return nil, searchSnapshotSchemaError("script#__NEXT_DATA__", "required Next.js data script is missing")
		}
	}

	stateJSON, state, err := extractSearchPageState(nextData)
	if err != nil {
		if htmlSnapshot {
			if reason := searchSnapshotChallengeReason(document); reason != "" {
				return nil, &ChallengeError{StatusCode: http.StatusOK, Reason: reason}
			}
		}
		return nil, err
	}

	result, err := decodeSearchResponse(stateJSON, 0, searchSnapshotQueryState(state))
	if err != nil {
		if htmlSnapshot {
			if reason := searchSnapshotChallengeReason(document); reason != "" {
				return nil, &ChallengeError{StatusCode: http.StatusOK, Reason: reason}
			}
		}
		return nil, err
	}
	if options.IncludeRaw {
		result.Raw = append(json.RawMessage(nil), stateJSON...)
	}
	return result, nil
}

// ParseSearchSnapshotWithOptions is an alias for ReadSearchSnapshotWithOptions.
func ParseSearchSnapshotWithOptions(reader io.Reader, options SearchSnapshotOptions) (*SearchResult, error) {
	return ReadSearchSnapshotWithOptions(reader, options)
}

func extractSearchPageState(nextData []byte) (json.RawMessage, map[string]any, error) {
	root, err := decodeSearchSnapshotObject(nextData, "__NEXT_DATA__")
	if err != nil {
		return nil, nil, err
	}
	props, err := searchSnapshotChildObject(root, "props", "props")
	if err != nil {
		return nil, nil, err
	}
	pageProps, err := searchSnapshotChildObject(props, "pageProps", "props.pageProps")
	if err != nil {
		return nil, nil, err
	}

	stateJSON, ok := pageProps["searchPageState"]
	if !ok {
		return nil, nil, searchSnapshotSchemaError("props.pageProps.searchPageState", "required search state is missing")
	}
	stateJSON = bytes.TrimSpace(stateJSON)
	if len(stateJSON) == 0 || bytes.Equal(stateJSON, []byte("null")) {
		return nil, nil, searchSnapshotSchemaError("props.pageProps.searchPageState", "search state must be an object")
	}

	value, err := decodeJSONValue(stateJSON)
	if err != nil {
		return nil, nil, searchSnapshotSchemaError("props.pageProps.searchPageState", "invalid JSON: "+err.Error())
	}
	state, ok := value.(map[string]any)
	if !ok {
		return nil, nil, searchSnapshotSchemaError("props.pageProps.searchPageState", "search state must be an object")
	}
	return append(json.RawMessage(nil), stateJSON...), state, nil
}

func decodeSearchSnapshotObject(data []byte, path string) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	var object map[string]json.RawMessage
	if err := decoder.Decode(&object); err != nil {
		return nil, searchSnapshotSchemaError(path, "invalid JSON object: "+err.Error())
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, searchSnapshotSchemaError(path, "multiple JSON values")
		}
		return nil, searchSnapshotSchemaError(path, "invalid trailing JSON: "+err.Error())
	}
	if object == nil {
		return nil, searchSnapshotSchemaError(path, "value must be an object")
	}
	return object, nil
}

func searchSnapshotChildObject(parent map[string]json.RawMessage, key, path string) (map[string]json.RawMessage, error) {
	raw, ok := parent[key]
	if !ok {
		return nil, searchSnapshotSchemaError(path, "required object is missing")
	}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return nil, searchSnapshotSchemaError(path, "value must be an object")
	}
	return decodeSearchSnapshotObject(raw, path)
}

func searchSnapshotQueryState(state map[string]any) map[string]any {
	for _, key := range []string{"searchQueryState", "queryState"} {
		if nested, ok := state[key].(map[string]any); ok {
			return nested
		}
	}
	return state
}

func looksLikeSearchSnapshotHTML(data []byte) bool {
	return isHTMLResponse("", data) || len(data) > 0 && data[0] == '<'
}

func searchSnapshotChallengeReason(document []byte) string {
	if reason := detectChallenge(responseHTML, http.StatusOK, "text/html", document); reason != "" {
		return reason
	}

	// detectChallenge intentionally trusts any __NEXT_DATA__ marker on successful
	// HTML. Snapshot ingestion can distinguish a real script element, so retain
	// challenge detection even when a marker occurs only in a comment or script.
	lower := strings.ToLower(string(document))
	markers := []struct {
		value  string
		reason string
	}{
		{value: "px-captcha", reason: "PerimeterX CAPTCHA page"},
		{value: "press & hold", reason: "press-and-hold challenge page"},
		{value: "verify you are human", reason: "human verification page"},
		{value: "access to this page has been denied", reason: "access-denied challenge page"},
		{value: "cf-chl-", reason: "browser challenge page"},
		{value: "challenge-platform", reason: "browser challenge page"},
		{value: "robot check", reason: "robot-check challenge page"},
		{value: "captcha", reason: "CAPTCHA page"},
	}
	for _, marker := range markers {
		if strings.Contains(lower, marker.value) {
			return marker.reason
		}
	}
	return ""
}

func searchSnapshotSchemaError(path, detail string) error {
	return &SchemaDriftError{Operation: "search snapshot", Path: path, Detail: detail}
}
