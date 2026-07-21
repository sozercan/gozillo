package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"gozillo/internal/zillow"
)

func TestSearchCommandAnnotatesPreviousJSONLResults(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	snapshotPath := filepath.Join(dir, "search.json")
	previousPath := filepath.Join(dir, "previous.jsonl")
	nextData := map[string]any{
		"props": map[string]any{
			"pageProps": map[string]any{
				"searchPageState": map[string]any{
					"queryState": map[string]any{},
					"cat1": map[string]any{
						"searchResults": map[string]any{"listResults": []any{
							map[string]any{"zpid": "changed", "unformattedPrice": 4800, "statusType": "FOR_RENT"},
							map[string]any{"zpid": "new", "unformattedPrice": 4500, "statusType": "FOR_RENT"},
						}},
						"searchList": map[string]any{"totalResultCount": 2},
					},
				},
			},
		},
	}
	encoded, err := json.Marshal(nextData)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(snapshotPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	previousPrice := int64(5000)
	previous := locatedListing{Location: "Example CA", Listing: &zillow.Listing{ID: "changed", Price: &previousPrice, Status: "FOR_RENT"}}
	var previousLine bytes.Buffer
	if err := json.NewEncoder(&previousLine).Encode(previous); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(previousPath, previousLine.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Execute([]string{"--output=jsonl", "search", "--snapshot", snapshotPath, "--previous-results", previousPath}, &stdout, &stderr)
	if code != ExitOK {
		t.Fatalf("code = %d, stderr = %q", code, stderr.String())
	}
	decoder := json.NewDecoder(&stdout)
	var first, second zillow.Listing
	if err := decoder.Decode(&first); err != nil {
		t.Fatal(err)
	}
	if err := decoder.Decode(&second); err != nil {
		t.Fatal(err)
	}
	if first.ID != "changed" || first.HistoryStatus != zillow.HistoryPreviouslyChanged || len(first.HistoryChanges) != 1 || first.HistoryChanges[0] != "price decreased" {
		t.Fatalf("first = %+v", first)
	}
	if second.ID != "new" || second.HistoryStatus != zillow.HistoryNewToDigest {
		t.Fatalf("second = %+v", second)
	}
}

func TestReadPreviousListingsSkipsPriorErrorRows(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "previous.jsonl")
	content := "{\"location\":\"First CA\",\"error\":\"temporary failure\"}\n" +
		"{\"location\":\"Second CA\",\"listing\":{\"id\":\"ok\"}}\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := readPreviousListings(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "ok" {
		t.Fatalf("previous listings = %+v", got)
	}
}
