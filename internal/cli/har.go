package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"gozillo/internal/cdphar"
	"gozillo/internal/har"
	"gozillo/internal/output"
)

type harCommand struct{}

func (harCommand) Name() string    { return "har" }
func (harCommand) Summary() string { return "Capture, sanitize, and derive Zillow HAR data" }

func (harCommand) Run(ctx Context, args []string) error {
	if len(args) == 0 || wantsHelp(args) {
		writeHARUsage(ctx.Stdout)
		return nil
	}

	switch args[0] {
	case "capture":
		return runHARCapture(ctx, args[1:])
	case "sanitize":
		return runHARSanitize(ctx, args[1:])
	case "candidates":
		return runHARCandidates(ctx, args[1:])
	case "derive":
		return runHARDerive(ctx, args[1:])
	default:
		return usagef("unknown har subcommand %q", args[0])
	}
}

func runHARCapture(ctx Context, args []string) error {
	flags := flag.NewFlagSet("har capture", flag.ContinueOnError)
	flags.SetOutput(ctx.Stderr)
	endpoint := flags.String("cdp", cdphar.DefaultEndpoint, "Chrome DevTools Protocol endpoint")
	outPath := flags.String("out", "", "output raw HAR path")
	wait := flags.Duration("wait", cdphar.DefaultWait, "time to keep recording after page load")
	timeout := flags.Duration("timeout", cdphar.DefaultTimeout, "maximum time for connection, navigation, and capture")
	responseBodies := flags.Bool("response-bodies", false, "attempt to include response bodies (can be large and sensitive)")
	allowRemote := flags.Bool("allow-remote-cdp", false, "allow a non-loopback CDP endpoint")
	if err := flags.Parse(args); err != nil {
		return &usageError{err: err}
	}
	if flags.NArg() != 1 {
		return usagef("har capture requires exactly one http(s) URL")
	}
	if strings.TrimSpace(*outPath) == "" {
		return usagef("har capture requires --out")
	}
	if *outPath == "-" {
		return usagef("har capture requires a file path; raw HAR output to stdout is disabled")
	}
	if *wait < 0 {
		return usagef("har capture --wait must be non-negative")
	}
	if *timeout <= 0 {
		return usagef("har capture --timeout must be positive")
	}
	if *wait >= *timeout {
		return usagef("har capture --wait must be shorter than --timeout")
	}
	if runtime.GOOS == "windows" {
		return fmt.Errorf("har capture: owner-only raw HAR writes are unsupported on Windows")
	}

	archive, err := cdphar.Capture(context.Background(), cdphar.Options{
		Endpoint:              *endpoint,
		URL:                   flags.Arg(0),
		Wait:                  *wait,
		Timeout:               *timeout,
		IncludeResponseBodies: *responseBodies,
		AllowRemoteEndpoint:   *allowRemote,
		CreatorName:           Name,
		CreatorVersion:        currentVersion(),
	})
	if err != nil {
		return err
	}
	if err := har.SaveFile(*outPath, archive); err != nil {
		return err
	}
	_, err = fmt.Fprintf(ctx.Stdout, "captured %d HAR entries -> %s\n", len(archive.Log.Entries), *outPath)
	return err
}

func runHARSanitize(ctx Context, args []string) error {
	flags := flag.NewFlagSet("har sanitize", flag.ContinueOnError)
	flags.SetOutput(ctx.Stderr)
	outPath := flags.String("out", "", "output sanitized HAR path")
	keepResponse := flags.Bool("keep-response-bodies", false, "retain response bodies (may contain sensitive data)")
	redaction := flags.String("redaction", har.DefaultRedaction, "redaction marker")
	var additional stringListFlag
	flags.Var(&additional, "sensitive-key", "additional JSON/query key to redact; repeatable")
	if err := flags.Parse(args); err != nil {
		return &usageError{err: err}
	}
	if flags.NArg() != 1 {
		return usagef("har sanitize requires exactly one input HAR")
	}
	if strings.TrimSpace(*outPath) == "" {
		return usagef("har sanitize requires --out")
	}

	archive, err := har.LoadFile(flags.Arg(0))
	if err != nil {
		return err
	}
	sanitized, err := har.Sanitize(archive, har.SanitizeOptions{
		KeepResponseBodies:      *keepResponse,
		Redaction:               *redaction,
		AdditionalSensitiveKeys: additional,
	})
	if err != nil {
		return err
	}
	if err := har.SaveFile(*outPath, sanitized); err != nil {
		return err
	}
	_, err = fmt.Fprintf(ctx.Stdout, "sanitized %d HAR entries -> %s\n", len(sanitized.Log.Entries), *outPath)
	return err
}

func runHARCandidates(ctx Context, args []string) error {
	flags := flag.NewFlagSet("har candidates", flag.ContinueOnError)
	flags.SetOutput(ctx.Stderr)
	limit := flags.Int("limit", 20, "maximum candidates to print (0 prints all)")
	if err := flags.Parse(args); err != nil {
		return &usageError{err: err}
	}
	if flags.NArg() != 1 {
		return usagef("har candidates requires exactly one HAR")
	}
	if *limit < 0 {
		return usagef("har candidates --limit must be non-negative")
	}

	archive, err := har.LoadFile(flags.Arg(0))
	if err != nil {
		return err
	}
	candidates, err := har.RankCandidates(archive)
	if err != nil {
		return err
	}
	if *limit > 0 && len(candidates) > *limit {
		candidates = candidates[:*limit]
	}

	printer, err := output.NewPrinter(ctx.Stdout, ctx.OutputMode)
	if err != nil {
		return err
	}
	switch ctx.OutputMode {
	case output.ModeJSONL:
		for _, candidate := range candidates {
			if err := printer.Print(candidate); err != nil {
				return err
			}
		}
		return nil
	case output.ModeTable:
		return printer.Print(candidateTable(candidates))
	default:
		return printer.Print(candidates)
	}
}

func runHARDerive(ctx Context, args []string) error {
	flags := flag.NewFlagSet("har derive", flag.ContinueOnError)
	flags.SetOutput(ctx.Stderr)
	outPath := flags.String("out", "", "output profile path; omit to write JSON to stdout")
	entryIndex := flags.Int("entry", -1, "specific HAR entry index; default selects the best candidate")
	if err := flags.Parse(args); err != nil {
		return &usageError{err: err}
	}
	if flags.NArg() != 1 {
		return usagef("har derive requires exactly one HAR")
	}
	if *entryIndex < -1 {
		return usagef("har derive --entry must be non-negative")
	}

	archive, err := har.LoadFile(flags.Arg(0))
	if err != nil {
		return err
	}
	var template *har.SearchTemplate
	if *entryIndex >= 0 {
		template, err = har.DeriveSearchTemplateAt(archive, *entryIndex)
	} else {
		template, err = har.DeriveSearchTemplate(archive)
	}
	if err != nil {
		return err
	}

	if strings.TrimSpace(*outPath) == "" || *outPath == "-" {
		return har.SaveSearchTemplate(ctx.Stdout, template)
	}
	if err := saveSearchTemplateFile(*outPath, template); err != nil {
		return err
	}
	_, err = fmt.Fprintf(ctx.Stdout, "derived search profile -> %s\n", *outPath)
	return err
}

func writeHARUsage(w io.Writer) {
	_, _ = io.WriteString(w, `Usage:
  gozillo [global options] har <subcommand> [options]
  gozillo har capture [capture options] --out <raw.har> <http(s)-url>

Capture options:
  --cdp <endpoint>       CDP HTTP endpoint or exact browser WebSocket URL
                         (default: http://127.0.0.1:9222)
  --out <path>           Required raw HAR output path
  --wait <duration>      Keep recording after page load (default: 5s)
  --timeout <duration>   Bound connection, navigation, and capture (default: 45s)
  --response-bodies      Attempt to include response bodies (best effort)
  --allow-remote-cdp     Permit a non-loopback exact WebSocket URL (high trust)

Raw captures can contain cookies and credential headers. They are written only
to an owner-only file and are never emitted on stdout. Capture covers the
primary page target; child worker/OOPIF target traffic may be omitted.

Subcommands:
  capture     Open a new browser tab through CDP and save a raw HAR
  sanitize    Remove cookies, credential headers, sensitive values, and response bodies
  candidates  Rank likely Zillow data requests
  derive      Derive a reusable search profile

Examples:
  gozillo har capture --cdp http://127.0.0.1:9222 --out search.raw.har https://www.zillow.com/
  gozillo har sanitize --out search.sanitized.har search.raw.har
  gozillo har candidates search.sanitized.har
  gozillo har derive --out search.profile.json search.sanitized.har
`)
}

func candidateTable(candidates []har.Candidate) output.Table {
	rows := make([][]string, 0, len(candidates))
	for _, candidate := range candidates {
		rows = append(rows, []string{
			strconv.Itoa(candidate.EntryIndex),
			strconv.Itoa(candidate.Score),
			candidate.Method,
			candidate.Host,
			candidate.Path,
			candidate.ResourceType,
			strings.Join(candidate.Reasons, "; "),
		})
	}
	return output.Table{
		Headers: []string{"ENTRY", "SCORE", "METHOD", "HOST", "PATH", "TYPE", "REASONS"},
		Rows:    rows,
	}
}

type stringListFlag []string

func (values *stringListFlag) String() string { return strings.Join(*values, ",") }
func (values *stringListFlag) Set(value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return fmt.Errorf("sensitive key must not be empty")
	}
	*values = append(*values, value)
	return nil
}

func saveSearchTemplateFile(path string, template *har.SearchTemplate) (err error) {
	if runtime.GOOS == "windows" {
		return fmt.Errorf("save search profile: owner-only file writes are unsupported on Windows")
	}
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary search profile: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		if err != nil {
			_ = os.Remove(temporaryPath)
		}
	}()

	if err = temporary.Chmod(0o600); err != nil {
		return fmt.Errorf("restrict temporary search profile permissions: %w", err)
	}
	if err = har.SaveSearchTemplate(temporary, template); err != nil {
		return err
	}
	if err = temporary.Sync(); err != nil {
		return fmt.Errorf("sync temporary search profile: %w", err)
	}
	if err = temporary.Close(); err != nil {
		return fmt.Errorf("close temporary search profile: %w", err)
	}
	if err = os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace search profile: %w", err)
	}
	return nil
}
