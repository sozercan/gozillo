package har

import (
	"bytes"
	"net/url"
	"strings"
	"testing"
)

func TestRankCandidatesGolden(t *testing.T) {
	t.Parallel()

	candidates, err := RankCandidates(sanitizeSynthetic(t))
	if err != nil {
		t.Fatalf("RankCandidates() error = %v", err)
	}
	if len(candidates) != 5 {
		t.Fatalf("candidate count = %d, want 5", len(candidates))
	}
	if candidates[0].EntryIndex != 2 {
		t.Fatalf("top candidate entry = %d, want 2", candidates[0].EntryIndex)
	}
	if candidates[0].Score <= candidates[1].Score {
		t.Fatalf("search endpoint score %d did not outrank generic JSON score %d", candidates[0].Score, candidates[1].Score)
	}
	if candidates[len(candidates)-1].EntryIndex != 4 {
		t.Fatalf("last candidate entry = %d, want telemetry entry 4", candidates[len(candidates)-1].EntryIndex)
	}

	var output bytes.Buffer
	if err := saveIndentedJSON(&output, candidates); err != nil {
		t.Fatalf("encode candidates: %v", err)
	}
	assertGolden(t, "candidates.golden.json", output.Bytes())
}

func TestRankCandidatesRedactsSensitiveURLParts(t *testing.T) {
	t.Parallel()

	candidates, err := RankCandidates(loadSynthetic(t))
	if err != nil {
		t.Fatalf("RankCandidates() error = %v", err)
	}
	if strings.Contains(candidates[0].URL, "synthetic-key") || strings.Contains(candidates[0].URL, "synthetic-fragment") {
		t.Fatalf("candidate URL leaked sensitive capture data: %q", candidates[0].URL)
	}
}

func TestRankCandidatesRejectsNilArchive(t *testing.T) {
	t.Parallel()

	if _, err := RankCandidates(nil); err == nil {
		t.Fatal("RankCandidates(nil) error = nil")
	}
}

func TestFirstPartyZillowHostRejectsLookalikes(t *testing.T) {
	t.Parallel()

	for _, host := range []string{"zillow.com.example.test", "notzillow.com", "www.zillowstatic.com"} {
		if isFirstPartyZillowHost(host) {
			t.Fatalf("isFirstPartyZillowHost(%q) = true", host)
		}
	}
	for _, host := range []string{"zillow.com", "www.zillow.com", "api.sub.zillow.com."} {
		if !isFirstPartyZillowHost(host) {
			t.Fatalf("isFirstPartyZillowHost(%q) = false", host)
		}
	}
}

func TestSearchEndpointBonusRequiresFirstPartyHost(t *testing.T) {
	t.Parallel()

	entry := Entry{
		Request: Request{
			Method: "PUT",
			URL:    "https://www.zillow.com.example.test/async-create-search-page-state",
			PostData: &PostData{
				MimeType: "application/json",
			},
		},
		Response:     Response{Status: 200, Content: Content{MimeType: "application/json"}},
		ResourceType: "xhr",
	}
	candidate := scoreCandidate(0, &entry)
	for _, reason := range candidate.Reasons {
		if reason == "+140 search-state endpoint" {
			t.Fatal("lookalike host received the first-party search endpoint bonus")
		}
	}
}

func TestRankCandidatesUsesSanitizedPath(t *testing.T) {
	t.Parallel()

	archive := &Archive{Log: Log{
		Version: Version12,
		Creator: Creator{Name: "test", Version: "1"},
		Entries: []Entry{{
			Request: Request{
				Method: "GET",
				URL:    "https://www.zillow.com/app;jsessionid=synthetic/token/synthetic-value",
			},
			Response: Response{Status: 200},
		}},
	}}

	candidates, err := RankCandidates(archive)
	if err != nil {
		t.Fatalf("RankCandidates() error = %v", err)
	}
	if strings.Contains(candidates[0].URL, "synthetic") || strings.Contains(candidates[0].Path, "synthetic") {
		t.Fatalf("candidate leaked path credentials: %#v", candidates[0])
	}
}

func TestRankCandidatesRedactsOpaqueAuthenticationURLParameters(t *testing.T) {
	t.Parallel()

	for _, parameterName := range []string{
		"code",
		"authorization_code",
		"oauth_code",
		"oauth_verifier",
		"code_verifier",
		"subscription-key",
		"Ocp-Apim-Subscription-Key",
		"state",
		"session_state",
		"nonce",
		"SaMl-ReSpOnSe",
		"sAmL_rEqUeSt",
		"ReLaY-StAtE",
		"ticket",
		"cas_ticket",
	} {
		parameterName := parameterName
		t.Run(parameterName, func(t *testing.T) {
			t.Parallel()

			archive := &Archive{Log: Log{
				Version: Version12,
				Creator: Creator{Name: "test", Version: "1"},
				Entries: []Entry{{
					Request: Request{
						Method: "GET",
						URL: "https://www.zillow.com/" + parameterName + "/opaque-path-value?" +
							url.QueryEscape(parameterName) + "=opaque-query-value",
					},
					Response: Response{Status: 200},
				}},
			}}

			candidates, err := RankCandidates(archive)
			if err != nil {
				t.Fatalf("RankCandidates() error = %v", err)
			}
			candidate := candidates[0]
			if strings.Contains(candidate.URL, "opaque-") || strings.Contains(candidate.Path, "opaque-") {
				t.Fatalf("candidate leaked opaque credential data: %#v", candidate)
			}
			parsed, err := url.Parse(candidate.URL)
			if err != nil {
				t.Fatal(err)
			}
			if got := parsed.Query().Get(parameterName); got != DefaultRedaction {
				t.Fatalf("candidate query value = %q, want %q", got, DefaultRedaction)
			}
			if got, want := parsed.Path, "/"+parameterName+"/"+DefaultRedaction; got != want {
				t.Fatalf("candidate path = %q, want %q", got, want)
			}
		})
	}
}

func TestRankCandidatesDoesNotSplitEscapedSensitivePathValues(t *testing.T) {
	t.Parallel()

	archive := &Archive{Log: Log{
		Version: Version12,
		Creator: Creator{Name: "test", Version: "1"},
		Entries: []Entry{{
			Request: Request{
				Method: "GET",
				URL:    "https://www.zillow.com/token/abc%2Fdef%3Bghi",
			},
			Response: Response{Status: 200},
		}},
	}}

	candidates, err := RankCandidates(archive)
	if err != nil {
		t.Fatalf("RankCandidates() error = %v", err)
	}
	candidate := candidates[0]
	if strings.Contains(candidate.URL, "abc") || strings.Contains(candidate.URL, "def") || strings.Contains(candidate.URL, "ghi") ||
		strings.Contains(candidate.Path, "abc") || strings.Contains(candidate.Path, "def") || strings.Contains(candidate.Path, "ghi") {
		t.Fatalf("candidate leaked an escaped sensitive path value: %#v", candidate)
	}
	if got, want := candidate.Path, "/token/%5BREDACTED%5D"; got != want {
		t.Fatalf("candidate path = %q, want %q", got, want)
	}
}
