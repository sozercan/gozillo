package zillow

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestFetchSearchPageParsesNextData(t *testing.T) {
	t.Parallel()

	nextData := map[string]any{
		"props": map[string]any{
			"pageProps": map[string]any{
				"searchPageState": map[string]any{
					"queryState": map[string]any{},
					"cat1": map[string]any{
						"searchResults": map[string]any{"listResults": []any{
							map[string]any{"zpid": "123", "address": "123 Coast Hwy", "unformattedPrice": 3200},
						}},
						"searchList": map[string]any{"totalResultCount": 1},
					},
				},
			},
		},
	}
	encoded, _ := json.Marshal(nextData)
	client, _ := newLocalZillowClient(t, http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/html")
		_, _ = writer.Write([]byte(`<html><script id="__NEXT_DATA__" type="application/json">` + string(encoded) + `</script></html>`))
	}))
	result, err := client.FetchSearchPage(context.Background(), "https://www.zillow.com/94044/rentals/")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Listings) != 1 || result.Listings[0].ID != "123" || result.Metadata.TotalResults != 1 {
		t.Fatalf("result = %+v", result)
	}
}
