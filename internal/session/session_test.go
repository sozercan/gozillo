package session

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"gozillo/internal/har"
)

func TestImportHARExtractsSuccessfulZillowRequestSession(t *testing.T) {
	t.Parallel()

	archive := syntheticSessionHAR()
	session, err := ImportHAR(archive)
	if err != nil {
		t.Fatal(err)
	}
	if session.UserAgent != "" {
		t.Fatalf("UserAgent = %q, want browser identity omitted", session.UserAgent)
	}
	if len(session.Cookies) != 3 {
		t.Fatalf("cookie count = %d, want 3: %+v", len(session.Cookies), session.Cookies)
	}
	values := map[string]string{}
	for _, cookie := range session.Cookies {
		values[cookie.Name] = cookie.Value
	}
	if values["zguid"] != "new" || values["zgsession"] != "fake" || values["search"] != "saved" {
		t.Fatalf("cookies = %#v", values)
	}
	if _, exists := values["blocked"]; exists {
		t.Fatal("cookie from blocked response was imported")
	}
}

func TestImportHARRejectsSanitizedOrBlockedCapture(t *testing.T) {
	t.Parallel()

	archive := &har.Archive{Log: har.Log{
		Version: har.Version12,
		Creator: har.Creator{Name: "test", Version: "1"},
		Entries: []har.Entry{{
			Request:  har.Request{Method: "GET", URL: "https://www.zillow.com/"},
			Response: har.Response{Status: 403},
		}},
	}}
	if _, err := ImportHAR(archive); err == nil {
		t.Fatal("ImportHAR() error = nil")
	}
}

func TestStoreOwnerOnlyRoundTripAndSummary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("owner-only plaintext sessions are unsupported on Windows")
	}
	store := &Store{Dir: filepath.Join(t.TempDir(), "sessions")}
	session, err := ImportHAR(syntheticSessionHAR())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save("default", session); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(store.path("default"))
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("session permissions = %04o, want 0600", got)
	}
	loaded, err := store.Load("default")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Cookies) != len(session.Cookies) || loaded.UserAgent != session.UserAgent {
		t.Fatalf("loaded session mismatch: %+v", loaded)
	}
	summaries, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 || summaries[0].Name != "default" || summaries[0].CookieCount != 3 {
		t.Fatalf("summaries = %+v", summaries)
	}
	if err := store.Remove("default"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load("default"); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Load after remove error = %v", err)
	}
}

func TestStoreRejectsBroadPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("owner-only plaintext sessions are unsupported on Windows")
	}
	store := &Store{Dir: filepath.Join(t.TempDir(), "sessions")}
	if err := store.ensureDirectory(); err != nil {
		t.Fatal(err)
	}
	path := store.path("unsafe")
	if err := os.WriteFile(path, []byte(`{"version":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Load("unsafe"); err == nil {
		t.Fatal("Load() error = nil for broadly readable session")
	}
}

func syntheticSessionHAR() *har.Archive {
	return &har.Archive{Log: har.Log{
		Version: har.Version12,
		Creator: har.Creator{Name: "test", Version: "1"},
		Entries: []har.Entry{
			{
				StartedDateTime: time.Now().Add(-time.Minute).Format(time.RFC3339),
				Request: har.Request{
					Method: "GET",
					URL:    "https://www.zillow.com/94044/rentals/",
					Headers: []har.NameValue{
						{Name: "User-Agent", Value: "Mozilla/5.0 Test Browser"},
						{Name: "Cookie", Value: "zgsession=fake; search=saved"},
					},
				},
				Response: har.Response{Status: 200},
			},
			{
				StartedDateTime: time.Now().Format(time.RFC3339),
				Request: har.Request{
					Method:  "GET",
					URL:     "https://www.zillow.com/94044/rentals/",
					Headers: []har.NameValue{{Name: "User-Agent", Value: "Mozilla/5.0 Test Browser"}},
					Cookies: []har.Cookie{{Name: "zguid", Value: "new", Domain: ".zillow.com", Path: "/", Secure: true}},
				},
				Response: har.Response{Status: 200},
			},
			{
				Request: har.Request{
					Method:  "GET",
					URL:     "https://www.zillow.com/blocked",
					Cookies: []har.Cookie{{Name: "blocked", Value: "fake", Domain: ".zillow.com", Path: "/"}},
				},
				Response: har.Response{Status: 403},
			},
		},
	}}
}
