package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestHARCaptureValidatesRequiredArgumentsBeforeConnecting(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want string
	}{
		{
			name: "missing URL",
			args: []string{"har", "capture", "--out", "capture.raw.har"},
			want: "requires exactly one http(s) URL",
		},
		{
			name: "missing output",
			args: []string{"har", "capture", "https://www.zillow.com/"},
			want: "requires --out",
		},
		{
			name: "stdout output disabled",
			args: []string{"har", "capture", "--out", "-", "https://www.zillow.com/"},
			want: "stdout is disabled",
		},
		{
			name: "negative wait",
			args: []string{"har", "capture", "--out", "capture.raw.har", "--wait", "-1s", "https://www.zillow.com/"},
			want: "--wait must be non-negative",
		},
		{
			name: "non-positive timeout",
			args: []string{"har", "capture", "--out", "capture.raw.har", "--timeout", "0s", "https://www.zillow.com/"},
			want: "--timeout must be positive",
		},
		{
			name: "wait not shorter than timeout",
			args: []string{"har", "capture", "--out", "capture.raw.har", "--wait", "2s", "--timeout", "2s", "https://www.zillow.com/"},
			want: "--wait must be shorter than --timeout",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var stdout bytes.Buffer
			var stderr bytes.Buffer
			if code := Execute(test.args, &stdout, &stderr); code != ExitUsage {
				t.Fatalf("Execute() code = %d, stderr = %q", code, stderr.String())
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout = %q, want empty", stdout.String())
			}
			if !strings.Contains(stderr.String(), test.want) {
				t.Fatalf("stderr = %q, want substring %q", stderr.String(), test.want)
			}
		})
	}
}

func TestHARUsageDocumentsCapture(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := Execute([]string{"har", "--help"}, &stdout, &stderr); code != ExitOK {
		t.Fatalf("Execute() code = %d, stderr = %q", code, stderr.String())
	}
	for _, want := range []string{"capture", "--cdp http://127.0.0.1:9222", "--allow-remote-cdp", "search.raw.har"} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("help = %q, want substring %q", stdout.String(), want)
		}
	}
}
