// Package session imports and stores the minimum browser session material used
// by gozillo. Session files are plaintext secrets and are always owner-only.
package session

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode"

	"gozillo/internal/har"
)

const Version = 1

var ErrUnsupportedPlatform = errors.New("owner-only session files are unsupported on Windows")

// Session is the minimal browser-derived state needed for read-only Zillow requests.
type Session struct {
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"createdAt"`
	// UserAgent is retained only for backward compatibility with version-1 files.
	// The CLI neither imports nor replays browser User-Agent values.
	UserAgent string   `json:"userAgent,omitempty"`
	Cookies   []Cookie `json:"cookies"`
}

// Cookie is the serializable subset of http.Cookie used by the client.
type Cookie struct {
	Name     string    `json:"name"`
	Value    string    `json:"value"`
	Domain   string    `json:"domain"`
	Path     string    `json:"path"`
	Expires  time.Time `json:"expires,omitempty"`
	Secure   bool      `json:"secure,omitempty"`
	HTTPOnly bool      `json:"httpOnly,omitempty"`
}

// Summary intentionally excludes cookie values and the User-Agent.
type Summary struct {
	Name        string    `json:"name"`
	CreatedAt   time.Time `json:"createdAt"`
	CookieCount int       `json:"cookieCount"`
	CookieNames []string  `json:"cookieNames,omitempty"`
}

// Store manages named session files.
type Store struct {
	Dir string
}

// DefaultStore returns the per-user file store. GOZILLO_CONFIG_DIR overrides
// the base config directory for automation and tests.
func DefaultStore() (*Store, error) {
	base := strings.TrimSpace(os.Getenv("GOZILLO_CONFIG_DIR"))
	if base == "" {
		var err error
		base, err = os.UserConfigDir()
		if err != nil {
			return nil, fmt.Errorf("resolve user config directory: %w", err)
		}
		base = filepath.Join(base, "gozillo")
	}
	return &Store{Dir: filepath.Join(base, "sessions")}, nil
}

// Path returns the validated file path for a named session.
func (store *Store) Path(name string) (string, error) {
	if err := validateName(name); err != nil {
		return "", err
	}
	if store == nil || strings.TrimSpace(store.Dir) == "" {
		return "", errors.New("session store directory is required")
	}
	return store.path(name), nil
}

// ImportHAR extracts cookies actually sent to successful first-party Zillow requests.
func ImportHAR(archive *har.Archive) (*Session, error) {
	if err := archive.Validate(); err != nil {
		return nil, fmt.Errorf("import session HAR: %w", err)
	}

	type capturedCookie struct {
		cookie Cookie
		order  int
	}
	cookies := make(map[string]capturedCookie)
	order := 0
	for _, entry := range archive.Log.Entries {
		parsed, err := url.Parse(entry.Request.URL)
		if err != nil || !isZillowCookieHost(parsed.Hostname()) {
			continue
		}
		if entry.Response.Status < 200 || entry.Response.Status >= 400 {
			continue
		}
		order++
		for _, captured := range entry.Request.Cookies {
			cookie, ok := cookieFromHAR(captured, parsed.Hostname())
			if ok {
				cookies[cookieKey(cookie)] = capturedCookie{cookie: cookie, order: order}
			}
		}
		if len(entry.Request.Cookies) == 0 {
			for _, value := range headerValues(entry.Request.Headers, "Cookie") {
				request := &http.Request{Header: http.Header{"Cookie": []string{value}}}
				for _, parsedCookie := range request.Cookies() {
					cookie := Cookie{
						Name:   parsedCookie.Name,
						Value:  parsedCookie.Value,
						Domain: parsed.Hostname(),
						Path:   "/",
					}
					if cookie.Valid() == nil {
						cookies[cookieKey(cookie)] = capturedCookie{cookie: cookie, order: order}
					}
				}
			}
		}
	}
	if len(cookies) == 0 {
		return nil, errors.New("import session HAR: no cookies found on a successful Zillow request; capture a normally loaded Zillow page")
	}

	ordered := make([]capturedCookie, 0, len(cookies))
	for _, cookie := range cookies {
		ordered = append(ordered, cookie)
	}
	sort.Slice(ordered, func(left, right int) bool {
		if ordered[left].order != ordered[right].order {
			return ordered[left].order < ordered[right].order
		}
		return cookieKey(ordered[left].cookie) < cookieKey(ordered[right].cookie)
	})
	result := &Session{
		Version:   Version,
		CreatedAt: time.Now().UTC(),
		Cookies:   make([]Cookie, 0, len(ordered)),
	}
	for _, item := range ordered {
		if !item.cookie.Expires.IsZero() && item.cookie.Expires.Before(time.Now()) {
			continue
		}
		result.Cookies = append(result.Cookies, item.cookie)
	}
	if len(result.Cookies) == 0 {
		return nil, errors.New("import session HAR: all captured Zillow cookies are expired")
	}
	if err := result.Validate(); err != nil {
		return nil, fmt.Errorf("import session HAR: %w", err)
	}
	return result, nil
}

// Validate checks the persisted secret format.
func (session *Session) Validate() error {
	if session == nil {
		return errors.New("session is nil")
	}
	if session.Version != Version {
		return fmt.Errorf("session version must be %d", Version)
	}
	if session.CreatedAt.IsZero() {
		return errors.New("session creation time is required")
	}
	if len(session.UserAgent) > 1024 || containsControl(session.UserAgent) {
		return errors.New("session User-Agent is invalid")
	}
	if len(session.Cookies) == 0 {
		return errors.New("session must contain at least one cookie")
	}
	if len(session.Cookies) > 256 {
		return errors.New("session contains too many cookies")
	}
	seen := make(map[string]struct{}, len(session.Cookies))
	for index, cookie := range session.Cookies {
		if err := cookie.Valid(); err != nil {
			return fmt.Errorf("session cookie %d: %w", index, err)
		}
		key := cookieKey(cookie)
		if _, exists := seen[key]; exists {
			return fmt.Errorf("session contains duplicate cookie %q", cookie.Name)
		}
		seen[key] = struct{}{}
	}
	return nil
}

// HTTPCookies returns copies suitable for a cookie jar.
func (session *Session) HTTPCookies() []*http.Cookie {
	if session == nil {
		return nil
	}
	cookies := make([]*http.Cookie, 0, len(session.Cookies))
	for _, item := range session.Cookies {
		cookies = append(cookies, &http.Cookie{
			Name:     item.Name,
			Value:    item.Value,
			Domain:   item.Domain,
			Path:     item.Path,
			Expires:  item.Expires,
			Secure:   item.Secure,
			HttpOnly: item.HTTPOnly,
		})
	}
	return cookies
}

// Save atomically writes a named owner-only plaintext session file.
func (store *Store) Save(name string, session *Session) error {
	if runtime.GOOS == "windows" {
		return ErrUnsupportedPlatform
	}
	if err := validateName(name); err != nil {
		return err
	}
	if err := session.Validate(); err != nil {
		return err
	}
	if err := store.ensureDirectory(); err != nil {
		return err
	}
	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(session); err != nil {
		return fmt.Errorf("encode session: %w", err)
	}
	temporary, err := os.CreateTemp(store.Dir, ".gozillo-session-*")
	if err != nil {
		return fmt.Errorf("create temporary session: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("restrict session permissions: %w", err)
	}
	if _, err := temporary.Write(encoded.Bytes()); err != nil {
		temporary.Close()
		return fmt.Errorf("write session: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("sync session: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close session: %w", err)
	}
	if err := os.Rename(temporaryPath, store.path(name)); err != nil {
		return fmt.Errorf("replace session: %w", err)
	}
	return nil
}

// Load reads and validates a named session without exposing it in logs.
func (store *Store) Load(name string) (*Session, error) {
	if runtime.GOOS == "windows" {
		return nil, ErrUnsupportedPlatform
	}
	if err := validateName(name); err != nil {
		return nil, err
	}
	path := store.path(name)
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("stat session %q: %w", name, err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("session %q permissions are too broad (%04o); require 0600", name, info.Mode().Perm())
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open session %q: %w", name, err)
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var session Session
	if err := decoder.Decode(&session); err != nil {
		return nil, fmt.Errorf("decode session %q: %w", name, err)
	}
	if err := session.Validate(); err != nil {
		return nil, fmt.Errorf("validate session %q: %w", name, err)
	}
	return &session, nil
}

// List returns summaries only; cookie values are never included.
func (store *Store) List() ([]Summary, error) {
	if runtime.GOOS == "windows" {
		return nil, ErrUnsupportedPlatform
	}
	if err := store.ensureDirectory(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(store.Dir)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	summaries := make([]Summary, 0)
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".json")
		session, err := store.Load(name)
		if err != nil {
			return nil, err
		}
		summaries = append(summaries, session.Summary(name))
	}
	sort.Slice(summaries, func(left, right int) bool { return summaries[left].Name < summaries[right].Name })
	return summaries, nil
}

// Remove deletes a named session file.
func (store *Store) Remove(name string) error {
	if runtime.GOOS == "windows" {
		return ErrUnsupportedPlatform
	}
	if err := validateName(name); err != nil {
		return err
	}
	if err := os.Remove(store.path(name)); err != nil {
		return fmt.Errorf("remove session %q: %w", name, err)
	}
	return nil
}

// Summary returns non-secret metadata.
func (session *Session) Summary(name string) Summary {
	names := make([]string, 0, len(session.Cookies))
	for _, cookie := range session.Cookies {
		names = append(names, cookie.Name)
	}
	sort.Strings(names)
	return Summary{Name: name, CreatedAt: session.CreatedAt, CookieCount: len(session.Cookies), CookieNames: names}
}

func (store *Store) ensureDirectory() error {
	if store == nil || strings.TrimSpace(store.Dir) == "" {
		return errors.New("session store directory is required")
	}
	if err := os.MkdirAll(store.Dir, 0o700); err != nil {
		return fmt.Errorf("create session directory: %w", err)
	}
	if err := os.Chmod(store.Dir, 0o700); err != nil {
		return fmt.Errorf("restrict session directory permissions: %w", err)
	}
	return nil
}

func (store *Store) path(name string) string {
	return filepath.Join(store.Dir, name+".json")
}

func (cookie Cookie) Valid() error {
	if !isZillowCookieHost(cookie.Domain) {
		return fmt.Errorf("cookie %q domain is not Zillow: %q", cookie.Name, cookie.Domain)
	}
	candidate := &http.Cookie{
		Name:     cookie.Name,
		Value:    cookie.Value,
		Domain:   cookie.Domain,
		Path:     cookie.Path,
		Expires:  cookie.Expires,
		Secure:   cookie.Secure,
		HttpOnly: cookie.HTTPOnly,
	}
	if err := candidate.Valid(); err != nil {
		return fmt.Errorf("cookie %q is not safe for Go HTTP transport: %w", cookie.Name, err)
	}
	return nil
}

func cookieFromHAR(captured har.Cookie, requestHost string) (Cookie, bool) {
	domain := strings.TrimSpace(captured.Domain)
	if domain == "" {
		domain = requestHost
	}
	path := captured.Path
	if path == "" {
		path = "/"
	}
	var expires time.Time
	if captured.Expires != "" {
		for _, layout := range []string{time.RFC3339Nano, time.RFC3339, http.TimeFormat} {
			if parsed, err := time.Parse(layout, captured.Expires); err == nil {
				expires = parsed
				break
			}
		}
	}
	cookie := Cookie{
		Name:     captured.Name,
		Value:    captured.Value,
		Domain:   domain,
		Path:     path,
		Expires:  expires,
		Secure:   captured.Secure,
		HTTPOnly: captured.HTTPOnly,
	}
	return cookie, cookie.Valid() == nil
}

func cookieKey(cookie Cookie) string {
	return strings.ToLower(cookie.Domain) + "\x00" + cookie.Path + "\x00" + cookie.Name
}

func validateName(name string) error {
	if name == "" || len(name) > 64 || name == "." || name == ".." {
		return errors.New("session name must be 1-64 safe characters")
	}
	for _, character := range name {
		if unicode.IsLetter(character) || unicode.IsNumber(character) || character == '-' || character == '_' || character == '.' {
			continue
		}
		return fmt.Errorf("session name contains unsupported character %q", character)
	}
	return nil
}

func isZillowCookieHost(host string) bool {
	host = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(host)), ".")
	return host == "zillow.com" || strings.HasSuffix(host, ".zillow.com")
}

func headerValue(headers []har.NameValue, name string) string {
	for _, header := range headers {
		if strings.EqualFold(header.Name, name) {
			return header.Value
		}
	}
	return ""
}

func headerValues(headers []har.NameValue, name string) []string {
	values := make([]string, 0)
	for _, header := range headers {
		if strings.EqualFold(header.Name, name) {
			values = append(values, header.Value)
		}
	}
	return values
}

func containsControl(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}
