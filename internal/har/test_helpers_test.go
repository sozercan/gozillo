package har

import (
	"bytes"
	"os"
	"testing"
)

func loadSynthetic(t *testing.T) *Archive {
	t.Helper()
	archive, err := LoadFile("testdata/synthetic.har")
	if err != nil {
		t.Fatalf("LoadFile(synthetic.har) error = %v", err)
	}
	return archive
}

func sanitizeSynthetic(t *testing.T) *Archive {
	t.Helper()
	archive, err := Sanitize(loadSynthetic(t))
	if err != nil {
		t.Fatalf("Sanitize() error = %v", err)
	}
	return archive
}

func assertGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	want, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", name, err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("golden mismatch for %s\n--- got ---\n%s\n--- want ---\n%s", name, got, want)
	}
}
