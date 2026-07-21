package har

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

func decodeJSON(text string) (any, error) {
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()

	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("multiple JSON values")
		}
		return nil, err
	}
	return value, nil
}

func marshalCompactJSON(value any) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return string(encoded), nil
}

func saveIndentedJSON(w io.Writer, value any) error {
	if w == nil {
		return fmt.Errorf("writer is nil")
	}
	encoder := json.NewEncoder(w)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

func isJSONMediaType(value string) bool {
	mediaType := strings.ToLower(strings.TrimSpace(strings.SplitN(value, ";", 2)[0]))
	return mediaType == "application/json" || mediaType == "text/json" || strings.HasSuffix(mediaType, "+json")
}

func looksLikeJSONObjectOrArray(text string) bool {
	trimmed := bytes.TrimSpace([]byte(text))
	return len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[')
}
