package har

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLoadSaveCanonicalHAR(t *testing.T) {
	t.Parallel()

	archive, err := LoadFile("testdata/sanitized.golden.har")
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	if archive.Log.Version != Version12 {
		t.Fatalf("version = %q, want %q", archive.Log.Version, Version12)
	}
	if len(archive.Log.Entries) != 5 {
		t.Fatalf("entries = %d, want 5", len(archive.Log.Entries))
	}

	var output bytes.Buffer
	if err := Save(&output, archive); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	assertGolden(t, "sanitized.golden.har", output.Bytes())

	roundTripped, err := Load(bytes.NewReader(output.Bytes()))
	if err != nil {
		t.Fatalf("Load(round trip) error = %v", err)
	}
	if got := roundTripped.Log.Entries[2].Request.Method; got != "PUT" {
		t.Fatalf("round-tripped method = %q, want PUT", got)
	}
	if got := roundTripped.Log.Entries[2].ResourceType; got != "xhr" {
		t.Fatalf("round-tripped resource type = %q, want xhr", got)
	}
}

func TestLoadRejectsInvalidDocuments(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  string
	}{
		{name: "missing version", input: `{"log":{"entries":[]}}`, want: "HAR log.version is required"},
		{name: "wrong version", input: `{"log":{"version":"1.1","entries":[]}}`, want: `unsupported HAR version "1.1"; want "1.2"`},
		{name: "trailing document", input: `{"log":{"version":"1.2","entries":[]}} {}`, want: "multiple JSON values"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := Load(strings.NewReader(test.input))
			if err == nil {
				t.Fatal("Load() error = nil, want validation error")
			}
			if !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Load() error = %q, want substring %q", err, test.want)
			}
		})
	}
}

func TestSaveFileUsesOwnerOnlyPermissions(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "capture.har")
	if runtime.GOOS == "windows" {
		if err := SaveFile(path, loadSynthetic(t)); err == nil || !strings.Contains(err.Error(), "unsupported on Windows") {
			t.Fatalf("SaveFile() error = %v, want explicit unsupported error", err)
		}
		return
	}
	if err := SaveFile(path, loadSynthetic(t)); err != nil {
		t.Fatalf("SaveFile() error = %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("permissions = %04o, want 0600", got)
	}
}

func TestSaveEmitsRequiredHARFieldsAndEmptyArrays(t *testing.T) {
	t.Parallel()

	archive := &Archive{Log: Log{
		Version: Version12,
		Creator: Creator{Name: "test", Version: "1"},
		Entries: []Entry{{
			StartedDateTime: "2026-07-19T12:00:00Z",
			Request: Request{
				Method:      "GET",
				URL:         "https://www.zillow.com/",
				HTTPVersion: "HTTP/2",
			},
			Response: Response{
				Status:      200,
				StatusText:  "OK",
				HTTPVersion: "HTTP/2",
				Content:     Content{MimeType: "text/html"},
			},
		}},
	}}

	var output bytes.Buffer
	if err := Save(&output, archive); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	var document map[string]any
	if err := json.Unmarshal(output.Bytes(), &document); err != nil {
		t.Fatalf("Unmarshal(saved HAR) error = %v", err)
	}
	logObject := document["log"].(map[string]any)
	entry := logObject["entries"].([]any)[0].(map[string]any)
	request := entry["request"].(map[string]any)
	response := entry["response"].(map[string]any)

	for _, key := range []string{"startedDateTime", "time", "request", "response", "cache", "timings"} {
		if _, found := entry[key]; !found {
			t.Fatalf("saved entry is missing required key %q: %s", key, output.String())
		}
	}
	for _, key := range []string{"headers", "queryString", "cookies", "headersSize", "bodySize"} {
		if _, found := request[key]; !found {
			t.Fatalf("saved request is missing required key %q: %s", key, output.String())
		}
	}
	for _, key := range []string{"statusText", "httpVersion", "headers", "cookies", "redirectURL", "headersSize", "bodySize"} {
		if _, found := response[key]; !found {
			t.Fatalf("saved response is missing required key %q: %s", key, output.String())
		}
	}
	if request["cookies"] == nil || response["cookies"] == nil {
		t.Fatalf("required cookie arrays serialized as null: %s", output.String())
	}
}
