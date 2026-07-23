package cli

import (
	"bytes"
	"errors"
	"reflect"
	"testing"

	"gozillo/internal/output"
)

type stubCommand struct {
	name    string
	summary string
	run     func(Context, []string) error
}

func (c stubCommand) Name() string {
	return c.name
}

func (c stubCommand) Summary() string {
	return c.summary
}

func (c stubCommand) Run(ctx Context, args []string) error {
	return c.run(ctx, args)
}

func TestExecuteWritesDeterministicRootUsage(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	code := Execute(nil, &stdout, &stderr)

	if code != ExitOK {
		t.Fatalf("Execute() code = %d, want %d", code, ExitOK)
	}
	if stderr.String() != "" {
		t.Fatalf("Execute() stderr = %q, want empty", stderr.String())
	}

	const want = `Usage:
  gozillo [global options] <command> [arguments]

A pure-Go Zillow web client.

Global options:
  -o, --output <mode>  Output mode: table, json, or jsonl (default: table)
  -h, --help           Show this help

Commands:
  search    Search Zillow through pure Go HTTP or a saved snapshot
  property  Extract normalized Zillow property details
  har       Capture, sanitize, and derive Zillow HAR data
  session   Import and manage file-backed Zillow sessions
  version   Print the gozillo version
`
	if stdout.String() != want {
		t.Fatalf("Execute() stdout mismatch\n got: %q\nwant: %q", stdout.String(), want)
	}
}

func TestExecuteDispatchesRegisteredCommand(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	var gotMode output.Mode
	var gotArgs []string

	search := stubCommand{
		name:    "search",
		summary: "Search listings",
		run: func(ctx Context, args []string) error {
			gotMode = ctx.OutputMode
			gotArgs = append([]string(nil), args...)
			_, err := ctx.Stdout.Write([]byte("ok\n"))
			return err
		},
	}

	code := Execute(
		[]string{"--output=jsonl", "search", "San Francisco", "--limit=5"},
		&stdout,
		&stderr,
		search,
	)

	if code != ExitOK {
		t.Fatalf("Execute() code = %d, want %d; stderr = %q", code, ExitOK, stderr.String())
	}
	if gotMode != output.ModeJSONL {
		t.Fatalf("command output mode = %q, want %q", gotMode, output.ModeJSONL)
	}
	wantArgs := []string{"San Francisco", "--limit=5"}
	if !reflect.DeepEqual(gotArgs, wantArgs) {
		t.Fatalf("command args = %#v, want %#v", gotArgs, wantArgs)
	}
	if stdout.String() != "ok\n" {
		t.Fatalf("Execute() stdout = %q, want %q", stdout.String(), "ok\n")
	}
	if stderr.String() != "" {
		t.Fatalf("Execute() stderr = %q, want empty", stderr.String())
	}
}

func TestExecuteReportsUsageErrorsToStderr(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		args       []string
		wantStderr string
	}{
		{
			name:       "unknown command",
			args:       []string{"unknown"},
			wantStderr: "gozillo: unknown command \"unknown\"\n",
		},
		{
			name:       "missing required command flag",
			args:       []string{"search"},
			wantStderr: "gozillo: search: search requires exactly one of --profile, --snapshot, or --location\n",
		},
		{
			name:       "invalid output mode",
			args:       []string{"--output=yaml", "search"},
			wantStderr: "gozillo: unknown output mode \"yaml\" (want table, json, or jsonl)\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var stdout bytes.Buffer
			var stderr bytes.Buffer

			code := Execute(tt.args, &stdout, &stderr)

			if code != ExitUsage {
				t.Fatalf("Execute() code = %d, want %d", code, ExitUsage)
			}
			if stdout.String() != "" {
				t.Fatalf("Execute() stdout = %q, want empty", stdout.String())
			}
			if stderr.String() != tt.wantStderr {
				t.Fatalf("Execute() stderr = %q, want %q", stderr.String(), tt.wantStderr)
			}
		})
	}
}

func TestExecuteReportsCommandErrorsToStderr(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	search := stubCommand{
		name:    "search",
		summary: "Search listings",
		run: func(Context, []string) error {
			return errors.New("request failed")
		},
	}

	code := Execute([]string{"search"}, &stdout, &stderr, search)

	if code != ExitFailure {
		t.Fatalf("Execute() code = %d, want %d", code, ExitFailure)
	}
	if stdout.String() != "" {
		t.Fatalf("Execute() stdout = %q, want empty", stdout.String())
	}
	if stderr.String() != "gozillo: search: request failed\n" {
		t.Fatalf("Execute() stderr = %q, want command error", stderr.String())
	}
}

func TestNewRootRejectsDuplicateCommands(t *testing.T) {
	t.Parallel()

	command := stubCommand{
		name:    "search",
		summary: "Search listings",
		run:     func(Context, []string) error { return nil },
	}

	_, err := NewRoot(nil, nil, command, command)
	if err == nil {
		t.Fatal("NewRoot() error = nil, want duplicate command error")
	}
	if err.Error() != `register command: duplicate name "search"` {
		t.Fatalf("NewRoot() error = %q, want duplicate command error", err)
	}
}
