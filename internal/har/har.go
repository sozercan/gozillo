// Package har loads, sanitizes, ranks, and derives request templates from
// HTTP Archive (HAR) 1.2 captures.
package har

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
)

const Version12 = "1.2"

// Archive is the top-level HAR document.
type Archive struct {
	Log Log `json:"log"`
}

// Log contains HAR metadata and captured requests.
type Log struct {
	Version string   `json:"version"`
	Creator Creator  `json:"creator"`
	Browser *Creator `json:"browser,omitempty"`
	Pages   []Page   `json:"pages,omitempty"`
	Entries []Entry  `json:"entries"`
	Comment string   `json:"comment,omitempty"`
}

// Creator identifies the software that produced a HAR.
type Creator struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Comment string `json:"comment,omitempty"`
}

// Page is the minimal HAR page record needed to preserve page references.
type Page struct {
	StartedDateTime string      `json:"startedDateTime"`
	ID              string      `json:"id"`
	Title           string      `json:"title"`
	PageTimings     PageTimings `json:"pageTimings"`
	Comment         string      `json:"comment,omitempty"`
}

// PageTimings contains optional page lifecycle timings in milliseconds.
type PageTimings struct {
	OnContentLoad *float64 `json:"onContentLoad,omitempty"`
	OnLoad        *float64 `json:"onLoad,omitempty"`
	Comment       string   `json:"comment,omitempty"`
}

// Entry is one captured request/response exchange. ResourceType and Initiator
// preserve common Chromium extensions used when ranking fetch/XHR traffic.
type Entry struct {
	PageRef         string          `json:"pageref,omitempty"`
	StartedDateTime string          `json:"startedDateTime"`
	Time            float64         `json:"time"`
	Request         Request         `json:"request"`
	Response        Response        `json:"response"`
	Cache           json.RawMessage `json:"cache"`
	Timings         *Timings        `json:"timings"`
	ServerIPAddress string          `json:"serverIPAddress,omitempty"`
	Connection      string          `json:"connection,omitempty"`
	Comment         string          `json:"comment,omitempty"`
	ResourceType    string          `json:"_resourceType,omitempty"`
	Initiator       json.RawMessage `json:"_initiator,omitempty"`
}

// Timings contains HAR request timing phases in milliseconds.
type Timings struct {
	Blocked *float64 `json:"blocked,omitempty"`
	DNS     *float64 `json:"dns,omitempty"`
	Connect *float64 `json:"connect,omitempty"`
	Send    float64  `json:"send"`
	Wait    float64  `json:"wait"`
	Receive float64  `json:"receive"`
	SSL     *float64 `json:"ssl,omitempty"`
	Comment string   `json:"comment,omitempty"`
}

// Request is the minimal HAR request representation needed for sanitization
// and request-template derivation.
type Request struct {
	Method      string      `json:"method"`
	URL         string      `json:"url"`
	HTTPVersion string      `json:"httpVersion"`
	Headers     []NameValue `json:"headers"`
	QueryString []NameValue `json:"queryString"`
	Cookies     []Cookie    `json:"cookies"`
	HeadersSize int64       `json:"headersSize"`
	BodySize    int64       `json:"bodySize"`
	PostData    *PostData   `json:"postData,omitempty"`
	Comment     string      `json:"comment,omitempty"`
}

// Response is the minimal HAR response representation needed for ranking.
type Response struct {
	Status      int         `json:"status"`
	StatusText  string      `json:"statusText"`
	HTTPVersion string      `json:"httpVersion"`
	Headers     []NameValue `json:"headers"`
	Cookies     []Cookie    `json:"cookies"`
	Content     Content     `json:"content"`
	RedirectURL string      `json:"redirectURL"`
	HeadersSize int64       `json:"headersSize"`
	BodySize    int64       `json:"bodySize"`
	Comment     string      `json:"comment,omitempty"`
}

// NameValue represents a HAR header or query-string item.
type NameValue struct {
	Name    string `json:"name"`
	Value   string `json:"value"`
	Comment string `json:"comment,omitempty"`
}

// Cookie is a HAR cookie. Sanitized archives always clear cookie arrays.
type Cookie struct {
	Name     string `json:"name"`
	Value    string `json:"value"`
	Path     string `json:"path,omitempty"`
	Domain   string `json:"domain,omitempty"`
	Expires  string `json:"expires,omitempty"`
	HTTPOnly bool   `json:"httpOnly,omitempty"`
	Secure   bool   `json:"secure,omitempty"`
	SameSite string `json:"sameSite,omitempty"`
	Comment  string `json:"comment,omitempty"`
}

// PostData contains a captured request body.
type PostData struct {
	MimeType string          `json:"mimeType"`
	Params   []PostDataParam `json:"params,omitempty"`
	Text     string          `json:"text,omitempty"`
	Comment  string          `json:"comment,omitempty"`
}

// PostDataParam is a form or multipart request parameter.
type PostDataParam struct {
	Name        string `json:"name"`
	Value       string `json:"value,omitempty"`
	FileName    string `json:"fileName,omitempty"`
	ContentType string `json:"contentType,omitempty"`
	Comment     string `json:"comment,omitempty"`
}

// Content contains response metadata and an optional captured body.
type Content struct {
	Size        int64  `json:"size"`
	Compression *int64 `json:"compression,omitempty"`
	MimeType    string `json:"mimeType"`
	Text        string `json:"text,omitempty"`
	Encoding    string `json:"encoding,omitempty"`
	Comment     string `json:"comment,omitempty"`
}

// Validate checks the structural contract supported by this package.
func (a *Archive) Validate() error {
	if a == nil {
		return errors.New("HAR archive is nil")
	}
	if a.Log.Version == "" {
		return errors.New("HAR log.version is required")
	}
	if a.Log.Version != Version12 {
		return fmt.Errorf("unsupported HAR version %q; want %q", a.Log.Version, Version12)
	}
	return nil
}

// Load decodes one HAR 1.2 document. Unknown standard or vendor-specific
// fields are ignored so captures from different browser exporters remain usable.
func Load(r io.Reader) (*Archive, error) {
	if r == nil {
		return nil, errors.New("load HAR: reader is nil")
	}

	decoder := json.NewDecoder(r)
	var archive Archive
	if err := decoder.Decode(&archive); err != nil {
		return nil, fmt.Errorf("load HAR: decode: %w", err)
	}

	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, errors.New("load HAR: multiple JSON values")
		}
		return nil, fmt.Errorf("load HAR: trailing data: %w", err)
	}
	if err := archive.Validate(); err != nil {
		return nil, fmt.Errorf("load HAR: %w", err)
	}
	return &archive, nil
}

// LoadFile opens and decodes a HAR 1.2 file.
func LoadFile(path string) (*Archive, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("load HAR %q: %w", path, err)
	}
	defer file.Close()

	archive, err := Load(file)
	if err != nil {
		return nil, fmt.Errorf("load HAR %q: %w", path, err)
	}
	return archive, nil
}

// Save writes a deterministic, indented HAR JSON document.
func Save(w io.Writer, archive *Archive) error {
	if w == nil {
		return errors.New("save HAR: writer is nil")
	}
	if err := archive.Validate(); err != nil {
		return fmt.Errorf("save HAR: %w", err)
	}

	normalized, err := normalizeForSave(archive)
	if err != nil {
		return fmt.Errorf("save HAR: normalize: %w", err)
	}

	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(normalized); err != nil {
		return fmt.Errorf("save HAR: encode: %w", err)
	}
	return nil
}

// SaveFile atomically writes a HAR with owner-only permissions because an
// unsanitized capture can contain credentials.
func SaveFile(path string, archive *Archive) error {
	if runtime.GOOS == "windows" {
		return errors.New("save HAR: owner-only file writes are unsupported on Windows")
	}
	if err := archive.Validate(); err != nil {
		return fmt.Errorf("save HAR %q: %w", path, err)
	}

	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".gozillo-har-*")
	if err != nil {
		return fmt.Errorf("save HAR %q: create temporary file: %w", path, err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)

	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("save HAR %q: set permissions: %w", path, err)
	}
	if err := Save(temporary, archive); err != nil {
		temporary.Close()
		return fmt.Errorf("save HAR %q: %w", path, err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("save HAR %q: sync: %w", path, err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("save HAR %q: close: %w", path, err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("save HAR %q: rename: %w", path, err)
	}
	return nil
}

func normalizeForSave(archive *Archive) (*Archive, error) {
	encoded, err := json.Marshal(archive)
	if err != nil {
		return nil, err
	}

	var normalized Archive
	if err := json.Unmarshal(encoded, &normalized); err != nil {
		return nil, err
	}
	if normalized.Log.Entries == nil {
		normalized.Log.Entries = []Entry{}
	}
	for index := range normalized.Log.Entries {
		entry := &normalized.Log.Entries[index]
		if entry.Request.Headers == nil {
			entry.Request.Headers = []NameValue{}
		}
		if entry.Request.QueryString == nil {
			entry.Request.QueryString = []NameValue{}
		}
		if entry.Request.Cookies == nil {
			entry.Request.Cookies = []Cookie{}
		}
		if entry.Response.Headers == nil {
			entry.Response.Headers = []NameValue{}
		}
		if entry.Response.Cookies == nil {
			entry.Response.Cookies = []Cookie{}
		}
		if len(entry.Cache) == 0 || string(entry.Cache) == "null" {
			entry.Cache = json.RawMessage(`{}`)
		}
		if entry.Timings == nil {
			entry.Timings = &Timings{}
		}
	}
	return &normalized, nil
}

func cloneArchive(archive *Archive) (*Archive, error) {
	var buffer bytes.Buffer
	if err := Save(&buffer, archive); err != nil {
		return nil, err
	}
	clone, err := Load(&buffer)
	if err != nil {
		return nil, fmt.Errorf("clone HAR: %w", err)
	}
	return clone, nil
}
