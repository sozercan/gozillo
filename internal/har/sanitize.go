package har

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"unicode"
)

const DefaultRedaction = "[REDACTED]"

// SanitizeOptions controls the small set of behaviors that can safely vary.
type SanitizeOptions struct {
	// KeepResponseBodies is intentionally false by default. Enabling it can
	// retain listing data, identifiers, or server-reflected request values.
	KeepResponseBodies bool

	// Redaction replaces sensitive JSON and parameter values.
	Redaction string

	// AdditionalSensitiveKeys augments the built-in case-insensitive key set.
	AdditionalSensitiveKeys []string
}

var sensitiveKeyNames = map[string]struct{}{
	"auth":               {},
	"authentication":     {},
	"authorization":      {},
	"proxyauthorization": {},
	"bearer":             {},
	"jwt":                {},
	"sas":                {},
	"mfa":                {},
	"otp":                {},
	"pin":                {},
	"cookie":             {},
	"setcookie":          {},
	"password":           {},
	"passwd":             {},
	"pwd":                {},
	"secret":             {},
	"clientsecret":       {},
	"apisecret":          {},
	"privatekey":         {},
	"accesskey":          {},
	"secretkey":          {},
	"signingkey":         {},
	"signature":          {},
	"sig":                {},
	"passphrase":         {},
	"apikey":             {},
	"token":              {},
	"accesstoken":        {},
	"refreshtoken":       {},
	"idtoken":            {},
	"authtoken":          {},
	"bearertoken":        {},
	"csrf":               {},
	"xsrf":               {},
	"csrftoken":          {},
	"xcsrftoken":         {},
	"xsrftoken":          {},
	"session":            {},
	"sessionid":          {},
	"sessiontoken":       {},
	"deviceid":           {},
	"clientid":           {},
	"zuid":               {},
	"credential":         {},
	"credentials":        {},
	"email":              {},
	"emailaddress":       {},
	"phone":              {},
	"phonenumber":        {},
	"accountid":          {},
	"userid":             {},
}

// opaqueCredentialURLKeyNames contains credential-bearing and callback URL
// parameter names that are too generic to treat as sensitive JSON keys. They
// are matched after normalizeKey, so authorization_code, OAuth-Code, and
// SAML-Response become authorizationcode, oauthcode, and samlresponse.
var opaqueCredentialURLKeyNames = map[string]struct{}{
	"code":                    {},
	"authcode":                {},
	"authorizationcode":       {},
	"oauthcode":               {},
	"oauth2code":              {},
	"oauthauthorizationcode":  {},
	"oauth2authorizationcode": {},
	"oauthconsumerkey":        {},
	"oauthnonce":              {},
	"oauthstate":              {},
	"oauthtoken":              {},
	"oauthtokensecret":        {},
	"oauthverifier":           {},
	"codeverifier":            {},
	"subscriptionkey":         {},
	"ocpapimsubscriptionkey":  {},
	"key":                     {},
	"state":                   {},
	"sessionstate":            {},
	"nonce":                   {},
	"samlresponse":            {},
	"samlrequest":             {},
	"relaystate":              {},
	"samlart":                 {},
	"samlartifact":            {},
	"ticket":                  {},
	"casticket":               {},
	"loginticket":             {},
	"proxyticket":             {},
	"serviceticket":           {},
}

var forbiddenHeaderNames = map[string]struct{}{
	"cookie":             {},
	"setcookie":          {},
	"auth":               {},
	"authentication":     {},
	"authorization":      {},
	"proxyauthorization": {},
	"bearer":             {},
	"jwt":                {},
	"sas":                {},
	"mfa":                {},
	"otp":                {},
	"pin":                {},
	"xapikey":            {},
	"xcsrftoken":         {},
	"xxsrftoken":         {},
}

var retainedRequestHeaders = map[string]struct{}{
	"accept":       {},
	"content-type": {},
	"origin":       {},
	"referer":      {},
	"referrer":     {},
}

var retainedResponseHeaders = map[string]struct{}{
	"content-type":     {},
	"location":         {},
	"content-location": {},
}

// Sanitize returns a deep copy of archive with credential-bearing headers and
// cookies removed, sensitive request values redacted, and response bodies
// removed unless explicitly retained.
func Sanitize(archive *Archive, options ...SanitizeOptions) (*Archive, error) {
	if len(options) > 1 {
		return nil, errors.New("sanitize HAR: at most one options value is allowed")
	}

	var opts SanitizeOptions
	if len(options) == 1 {
		opts = options[0]
	}
	if opts.Redaction == "" {
		opts.Redaction = DefaultRedaction
	}
	if err := validateRedaction(opts.Redaction); err != nil {
		return nil, fmt.Errorf("sanitize HAR: redaction: %w", err)
	}

	keys := make(map[string]struct{}, len(sensitiveKeyNames)+len(opts.AdditionalSensitiveKeys))
	for key := range sensitiveKeyNames {
		keys[key] = struct{}{}
	}
	for _, key := range opts.AdditionalSensitiveKeys {
		normalized := normalizeKey(key)
		if normalized == "" {
			return nil, fmt.Errorf("sanitize HAR: additional sensitive key %q is empty after normalization", key)
		}
		keys[normalized] = struct{}{}
	}

	clean, err := cloneArchive(archive)
	if err != nil {
		return nil, fmt.Errorf("sanitize HAR: %w", err)
	}
	clean.Log.Pages = nil
	clean.Log.Comment = ""
	clean.Log.Creator.Comment = ""
	if clean.Log.Browser != nil {
		clean.Log.Browser.Comment = ""
	}

	for index := range clean.Log.Entries {
		entry := &clean.Log.Entries[index]
		entry.PageRef = ""
		entry.Comment = ""
		entry.ServerIPAddress = ""
		entry.Connection = ""
		entry.Cache = json.RawMessage(`{}`)
		if entry.Timings != nil {
			entry.Timings.Comment = ""
		}
		entry.Request.Comment = ""
		entry.Response.Comment = ""
		entry.Response.Content.Comment = ""

		preserveSearchBody := isSearchStateRequest(&entry.Request)

		entry.Request.Headers = sanitizeHeaders(entry.Request.Headers, keys, retainedRequestHeaders)
		entry.Response.Headers = sanitizeHeaders(entry.Response.Headers, keys, retainedResponseHeaders)
		entry.Request.Cookies = []Cookie{}
		entry.Response.Cookies = []Cookie{}
		entry.Initiator = nil

		entry.Request.QueryString = sanitizeNameValues(entry.Request.QueryString, opts.Redaction, keys)
		entry.Request.URL, err = sanitizeURL(entry.Request.URL, opts.Redaction, keys)
		if err != nil {
			return nil, fmt.Errorf("sanitize HAR: entry %d request URL: %w", index, err)
		}

		for _, headers := range [][]NameValue{entry.Request.Headers, entry.Response.Headers} {
			for headerIndex := range headers {
				header := &headers[headerIndex]
				switch normalizeKey(header.Name) {
				case "referer", "referrer", "origin", "location", "contentlocation":
					header.Value, err = sanitizeURL(header.Value, opts.Redaction, keys)
					if err != nil {
						return nil, fmt.Errorf("sanitize HAR: entry %d %s header: %w", index, header.Name, err)
					}
				}
			}
		}

		if entry.Request.PostData != nil {
			if err := sanitizePostData(entry.Request.PostData, preserveSearchBody, opts.Redaction, keys); err != nil {
				return nil, fmt.Errorf("sanitize HAR: entry %d postData: %w", index, err)
			}
		}

		entry.Response.RedirectURL, err = sanitizeURL(entry.Response.RedirectURL, opts.Redaction, keys)
		if err != nil {
			return nil, fmt.Errorf("sanitize HAR: entry %d redirect URL: %w", index, err)
		}

		if !opts.KeepResponseBodies {
			entry.Response.Content.Text = ""
			entry.Response.Content.Encoding = ""
		}
	}

	if err := verifySanitized(clean, opts, keys); err != nil {
		return nil, fmt.Errorf("sanitize HAR: verification failed: %w", err)
	}
	return clean, nil
}

func sanitizeHeaders(headers []NameValue, keys, retained map[string]struct{}) []NameValue {
	clean := make([]NameValue, 0, len(headers))
	for _, header := range headers {
		canonical := strings.ToLower(strings.TrimSpace(header.Name))
		if _, allowed := retained[canonical]; !allowed || isForbiddenHeader(header.Name) || isSensitiveKey(header.Name, keys) {
			continue
		}
		header.Comment = ""
		clean = append(clean, header)
	}
	return clean
}

func sanitizeNameValues(values []NameValue, redaction string, keys map[string]struct{}) []NameValue {
	clean := append([]NameValue(nil), values...)
	for index := range clean {
		clean[index].Comment = ""
		if credentialLikeString(clean[index].Name) {
			clean[index].Name = redaction
			if clean[index].Value != "" {
				clean[index].Value = redaction
			}
			continue
		}
		if isSensitiveURLKey(clean[index].Name, keys) {
			clean[index].Value = redaction
			continue
		}
		if sanitized, ok := sanitizeJSONValue(clean[index].Value, redaction, keys).(string); ok {
			clean[index].Value = sanitized
		}
	}
	return clean
}

func sanitizeURL(rawURL, redaction string, keys map[string]struct{}) (string, error) {
	if strings.TrimSpace(rawURL) == "" {
		return rawURL, nil
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	if parsed.Opaque != "" {
		return "", errors.New("opaque URL cannot be sanitized")
	}
	parsed.User = nil
	parsed.Fragment = ""
	escapedPath, err := sanitizeURLPath(parsed.EscapedPath(), redaction, keys)
	if err != nil {
		return "", fmt.Errorf("sanitize path: %w", err)
	}
	parsed.Path, err = url.PathUnescape(escapedPath)
	if err != nil {
		return "", fmt.Errorf("decode sanitized path: %w", err)
	}
	parsed.RawPath = escapedPath

	parsed.RawQuery, err = sanitizeRawQuery(parsed.RawQuery, redaction, keys)
	if err != nil {
		// A malformed query is optional request metadata. Dropping it is safer
		// than either retaining an uninspectable value or aborting the whole HAR.
		parsed.RawQuery = ""
	}
	return parsed.String(), nil
}

func sanitizeURLPath(escapedValue, redaction string, keys map[string]struct{}) (string, error) {
	segments := strings.Split(escapedValue, "/")
	escapedRedaction := url.PathEscape(redaction)
	previousSensitive := false
	for segmentIndex, segment := range segments {
		parts := strings.Split(segment, ";")
		base := parts[0]
		decodedBase, err := url.PathUnescape(base)
		if err != nil {
			return "", fmt.Errorf("decode path segment %d: %w", segmentIndex, err)
		}

		currentSensitive := false
		if previousSensitive && base != "" {
			base = escapedRedaction
		} else {
			baseKey, baseValue, hasBaseValue := strings.Cut(base, "=")
			if hasBaseValue {
				decodedKey, err := url.PathUnescape(baseKey)
				if err != nil {
					return "", fmt.Errorf("decode path segment %d key: %w", segmentIndex, err)
				}
				decodedValue, err := url.PathUnescape(baseValue)
				if err != nil {
					return "", fmt.Errorf("decode path segment %d value: %w", segmentIndex, err)
				}
				if isSensitiveURLKey(decodedKey, keys) {
					base = baseKey + "=" + escapedRedaction
				} else if credentialLikeString(decodedKey) {
					base = escapedRedaction + "=" + escapedRedaction
				} else if sanitized, ok := sanitizeJSONValue(decodedValue, redaction, keys).(string); ok && sanitized != decodedValue {
					base = baseKey + "=" + url.PathEscape(sanitized)
				}
			} else {
				namedSensitive := isSensitiveURLKey(decodedBase, keys)
				credentialName := credentialLikeString(decodedBase)
				currentSensitive = namedSensitive || credentialName
				if credentialName && !namedSensitive {
					base = escapedRedaction
				}
			}
		}
		previousSensitive = currentSensitive
		parts[0] = base

		for partIndex := 1; partIndex < len(parts); partIndex++ {
			key, value, hasValue := strings.Cut(parts[partIndex], "=")
			decodedKey, err := url.PathUnescape(key)
			if err != nil {
				return "", fmt.Errorf("decode path segment %d matrix key %d: %w", segmentIndex, partIndex, err)
			}
			decodedValue := ""
			if hasValue {
				decodedValue, err = url.PathUnescape(value)
				if err != nil {
					return "", fmt.Errorf("decode path segment %d matrix value %d: %w", segmentIndex, partIndex, err)
				}
			}
			if isSensitiveURLKey(decodedKey, keys) {
				parts[partIndex] = key + "=" + escapedRedaction
			} else if credentialLikeString(decodedKey) {
				if hasValue {
					parts[partIndex] = escapedRedaction + "=" + escapedRedaction
				} else {
					parts[partIndex] = escapedRedaction
				}
			} else if hasValue {
				if sanitized, ok := sanitizeJSONValue(decodedValue, redaction, keys).(string); ok && sanitized != decodedValue {
					parts[partIndex] = key + "=" + url.PathEscape(sanitized)
				}
			} else if credentialLikeString(decodedKey) {
				parts[partIndex] = escapedRedaction
			}
		}
		segments[segmentIndex] = strings.Join(parts, ";")
	}
	return strings.Join(segments, "/"), nil
}

func sanitizeRawQuery(rawQuery, redaction string, keys map[string]struct{}) (string, error) {
	if rawQuery == "" {
		return "", nil
	}

	var output strings.Builder
	componentStart := 0
	for index := 0; index <= len(rawQuery); index++ {
		if index < len(rawQuery) && rawQuery[index] != '&' && rawQuery[index] != ';' {
			continue
		}

		component := rawQuery[componentStart:index]
		key, value, hasValue := strings.Cut(component, "=")
		decodedKey, err := url.QueryUnescape(key)
		if err != nil {
			return "", err
		}
		decodedValue := ""
		if hasValue {
			decodedValue, err = url.QueryUnescape(value)
			if err != nil {
				return "", err
			}
		}

		switch {
		case isSensitiveURLKey(decodedKey, keys):
			component = key + "=" + url.QueryEscape(redaction)
		case credentialLikeString(decodedKey):
			component = url.QueryEscape(redaction)
			if hasValue {
				component += "=" + url.QueryEscape(redaction)
			}
		case hasValue:
			if sanitized, ok := sanitizeJSONValue(decodedValue, redaction, keys).(string); ok && sanitized != decodedValue {
				component = key + "=" + url.QueryEscape(sanitized)
			}
		}

		output.WriteString(component)
		if index < len(rawQuery) {
			output.WriteByte(rawQuery[index])
		}
		componentStart = index + 1
	}
	return output.String(), nil
}

func isSearchStateRequest(request *Request) bool {
	if request == nil || request.Method != "PUT" {
		return false
	}
	_, err := parseSearchTemplateEndpoint(request.URL)
	return err == nil
}

func sanitizePostData(postData *PostData, preserveSearchBody bool, redaction string, keys map[string]struct{}) error {
	postData.Comment = ""
	for index := range postData.Params {
		parameter := &postData.Params[index]
		parameter.Comment = ""
		if credentialLikeString(parameter.Name) {
			parameter.Name = redaction
		}
		if parameter.Value != "" {
			parameter.Value = redaction
		}
		if parameter.FileName != "" {
			parameter.FileName = redaction
		}
	}

	if strings.TrimSpace(postData.Text) == "" {
		return nil
	}

	explicitJSON := isJSONMediaType(postData.MimeType)
	heuristicJSON := looksLikeJSONObjectOrArray(postData.Text)
	if !explicitJSON && !heuristicJSON {
		postData.Text = ""
		return nil
	}

	value, err := decodeJSON(postData.Text)
	if err != nil {
		if explicitJSON || preserveSearchBody {
			return fmt.Errorf("decode JSON body: %w", err)
		}
		postData.Text = ""
		return nil
	}
	if !preserveSearchBody {
		postData.Text = ""
		return nil
	}

	body, ok := value.(map[string]any)
	if !ok {
		return errors.New("Zillow search postData JSON must be an object")
	}
	searchQueryState, ok := body["searchQueryState"].(map[string]any)
	if !ok {
		return errors.New("Zillow search postData.searchQueryState must be an object")
	}
	wants, ok := body["wants"].(map[string]any)
	if !ok {
		return errors.New("Zillow search postData.wants must be an object")
	}

	minimal := map[string]any{
		"searchQueryState": searchQueryState,
		"wants":            wants,
	}
	redactJSON(minimal, redaction, keys)
	postData.Text, err = marshalCompactJSON(minimal)
	if err != nil {
		return fmt.Errorf("encode redacted JSON body: %w", err)
	}
	return nil
}

func redactJSON(value any, redaction string, keys map[string]struct{}) {
	switch typed := value.(type) {
	case map[string]any:
		for key, nested := range typed {
			if isSensitiveKey(key, keys) {
				typed[key] = redaction
				continue
			}
			typed[key] = sanitizeJSONValue(nested, redaction, keys)
		}
	case []any:
		for index, nested := range typed {
			typed[index] = sanitizeJSONValue(nested, redaction, keys)
		}
	}
}

func sanitizeJSONValue(value any, redaction string, keys map[string]struct{}) any {
	switch typed := value.(type) {
	case map[string]any, []any:
		redactJSON(typed, redaction, keys)
		return typed
	case string:
		if looksLikeJSONObjectOrArray(typed) {
			nested, err := decodeJSON(typed)
			if err != nil {
				return redaction
			}
			redactJSON(nested, redaction, keys)
			encoded, err := marshalCompactJSON(nested)
			if err != nil {
				return redaction
			}
			return encoded
		}
		if looksLikeURLValue(typed) {
			sanitized, err := sanitizeURL(typed, redaction, keys)
			if err != nil {
				return redaction
			}
			return sanitized
		}
		if credentialLikeString(typed) {
			return redaction
		}
	}
	return value
}

func looksLikeURLValue(value string) bool {
	value = strings.TrimSpace(value)
	lower := strings.ToLower(value)
	return strings.HasPrefix(lower, "http://") ||
		strings.HasPrefix(lower, "https://") ||
		strings.HasPrefix(value, "//") ||
		strings.HasPrefix(value, "/")
}

func validateRedaction(value string) error {
	if strings.ContainsAny(value, "/;") {
		return errors.New("must not contain URL path delimiters '/' or ';'")
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return errors.New("must not contain control characters")
		}
	}
	return nil
}

func decodedCredentialLike(value string) bool {
	decoded, err := url.QueryUnescape(value)
	if err == nil {
		return credentialLikeString(decoded)
	}
	return credentialLikeString(value)
}

func credentialLikeString(value string) bool {
	lower := strings.ToLower(strings.TrimSpace(value))
	for _, marker := range []string{
		"authorization:", "proxy-authorization:", "bearer ", "basic ",
		"cookie:", "set-cookie:", "access_token", "refresh_token", "id_token",
		"api_key", "apikey", "sessionid", "jsessionid", "csrf", "xsrf",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return looksLikeJWT(strings.TrimSpace(value))
}

func looksLikeJWT(value string) bool {
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		if len(part) < 8 {
			return false
		}
		for _, character := range part {
			if (character >= 'a' && character <= 'z') ||
				(character >= 'A' && character <= 'Z') ||
				(character >= '0' && character <= '9') || character == '-' || character == '_' {
				continue
			}
			return false
		}
	}
	return true
}

func verifySanitized(archive *Archive, opts SanitizeOptions, keys map[string]struct{}) error {
	if len(archive.Log.Pages) != 0 {
		return errors.New("page metadata was not removed")
	}
	if archive.Log.Comment != "" || archive.Log.Creator.Comment != "" ||
		(archive.Log.Browser != nil && archive.Log.Browser.Comment != "") {
		return errors.New("log metadata comments were not removed")
	}
	for entryIndex := range archive.Log.Entries {
		entry := &archive.Log.Entries[entryIndex]
		if entry.PageRef != "" {
			return fmt.Errorf("entry %d still contains a page reference", entryIndex)
		}
		if entry.Comment != "" || entry.ServerIPAddress != "" || entry.Connection != "" ||
			entry.Request.Comment != "" || entry.Response.Comment != "" || entry.Response.Content.Comment != "" ||
			(entry.Timings != nil && entry.Timings.Comment != "") {
			return fmt.Errorf("entry %d still contains free-form metadata", entryIndex)
		}
		if string(entry.Cache) != "{}" {
			return fmt.Errorf("entry %d still contains cache metadata", entryIndex)
		}
		if len(entry.Request.Cookies) != 0 || len(entry.Response.Cookies) != 0 {
			return fmt.Errorf("entry %d still contains cookies", entryIndex)
		}
		if len(entry.Initiator) != 0 {
			return fmt.Errorf("entry %d still contains initiator metadata", entryIndex)
		}
		if err := verifySanitizedURL(entry.Request.URL, opts.Redaction, keys); err != nil {
			return fmt.Errorf("entry %d request URL: %w", entryIndex, err)
		}
		if err := verifySanitizedURL(entry.Response.RedirectURL, opts.Redaction, keys); err != nil {
			return fmt.Errorf("entry %d redirect URL: %w", entryIndex, err)
		}
		for _, item := range entry.Request.QueryString {
			if item.Comment != "" {
				return fmt.Errorf("entry %d query parameter %q retained a comment", entryIndex, item.Name)
			}
			if credentialLikeString(item.Name) && item.Name != opts.Redaction {
				return fmt.Errorf("entry %d query parameter name was not redacted", entryIndex)
			}
			if (isSensitiveURLKey(item.Name, keys) || decodedCredentialLike(item.Value)) && item.Value != opts.Redaction {
				return fmt.Errorf("entry %d query parameter %q was not redacted", entryIndex, item.Name)
			}
		}
		for _, headers := range [][]NameValue{entry.Request.Headers, entry.Response.Headers} {
			for _, header := range headers {
				if header.Comment != "" {
					return fmt.Errorf("entry %d header %q retained a comment", entryIndex, header.Name)
				}
				if isForbiddenHeader(header.Name) || isSensitiveKey(header.Name, keys) {
					return fmt.Errorf("entry %d still contains sensitive header %q", entryIndex, header.Name)
				}
				switch normalizeKey(header.Name) {
				case "referer", "referrer", "origin", "location", "contentlocation":
					if err := verifySanitizedURL(header.Value, opts.Redaction, keys); err != nil {
						return fmt.Errorf("entry %d %s header: %w", entryIndex, header.Name, err)
					}
				}
			}
		}
		if !opts.KeepResponseBodies && (entry.Response.Content.Text != "" || entry.Response.Content.Encoding != "") {
			return fmt.Errorf("entry %d still contains a response body", entryIndex)
		}
		if entry.Request.PostData == nil {
			continue
		}
		if entry.Request.PostData.Comment != "" {
			return fmt.Errorf("entry %d postData retained a comment", entryIndex)
		}
		for _, parameter := range entry.Request.PostData.Params {
			if credentialLikeString(parameter.Name) && parameter.Name != opts.Redaction {
				return fmt.Errorf("entry %d postData parameter name was not redacted", entryIndex)
			}
			if parameter.Comment != "" {
				return fmt.Errorf("entry %d postData parameter %q retained a comment", entryIndex, parameter.Name)
			}
			if parameter.Value != "" && parameter.Value != opts.Redaction {
				return fmt.Errorf("entry %d postData parameter %q was not redacted", entryIndex, parameter.Name)
			}
			if parameter.FileName != "" && parameter.FileName != opts.Redaction {
				return fmt.Errorf("entry %d postData filename %q was not redacted", entryIndex, parameter.Name)
			}
		}
		if strings.TrimSpace(entry.Request.PostData.Text) == "" {
			continue
		}
		if !isJSONMediaType(entry.Request.PostData.MimeType) && !looksLikeJSONObjectOrArray(entry.Request.PostData.Text) {
			return fmt.Errorf("entry %d retained a non-JSON request body", entryIndex)
		}
		value, err := decodeJSON(entry.Request.PostData.Text)
		if err != nil {
			return fmt.Errorf("entry %d retained invalid JSON: %w", entryIndex, err)
		}
		if err := verifyRedactedJSON(value, opts.Redaction, keys); err != nil {
			return fmt.Errorf("entry %d: %w", entryIndex, err)
		}
	}
	return nil
}

func verifyRedactedJSON(value any, redaction string, keys map[string]struct{}) error {
	switch typed := value.(type) {
	case map[string]any:
		for key, nested := range typed {
			if isSensitiveKey(key, keys) {
				if text, ok := nested.(string); !ok || text != redaction {
					return fmt.Errorf("sensitive JSON key %q was not redacted", key)
				}
				continue
			}
			if err := verifyRedactedJSON(nested, redaction, keys); err != nil {
				return err
			}
		}
	case []any:
		for _, nested := range typed {
			if err := verifyRedactedJSON(nested, redaction, keys); err != nil {
				return err
			}
		}
	case string:
		if looksLikeJSONObjectOrArray(typed) {
			if nested, err := decodeJSON(typed); err == nil {
				if err := verifyRedactedJSON(nested, redaction, keys); err != nil {
					return err
				}
			}
		}
		if typed != redaction && credentialLikeString(typed) {
			return errors.New("credential-like JSON string was not redacted")
		}
	}
	return nil
}

func verifySanitizedURL(rawURL, redaction string, keys map[string]struct{}) error {
	if strings.TrimSpace(rawURL) == "" {
		return nil
	}
	clean, err := sanitizeURL(rawURL, redaction, keys)
	if err != nil {
		return err
	}
	if clean != rawURL {
		return errors.New("URL still contains sensitive or non-canonical data")
	}
	return nil
}

func isForbiddenHeader(name string) bool {
	if strings.HasPrefix(strings.TrimSpace(name), ":") {
		return true
	}
	_, found := forbiddenHeaderNames[normalizeKey(name)]
	return found
}

func isSensitiveKey(name string, keys map[string]struct{}) bool {
	normalized := normalizeKey(name)
	if _, found := keys[normalized]; found {
		return true
	}
	for _, suffix := range []string{"apikey", "accesskey", "privatekey", "secretkey", "signingkey", "password", "passphrase", "secret", "signature", "token", "credential", "authorization", "auth", "sessionid", "session", "accountid", "userid", "clientid", "zuid", "sig", "jwt", "sas", "bearer", "mfa", "otp", "pin", "email", "phone"} {
		if strings.HasSuffix(normalized, suffix) {
			return true
		}
	}
	return false
}

func isSensitiveURLKey(name string, keys map[string]struct{}) bool {
	if isSensitiveKey(name, keys) {
		return true
	}
	_, found := opaqueCredentialURLKeyNames[normalizeKey(name)]
	return found
}

func normalizeKey(value string) string {
	return strings.Map(func(character rune) rune {
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			return unicode.ToLower(character)
		}
		return -1
	}, strings.TrimSpace(value))
}
