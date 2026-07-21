package cli

import (
	"flag"
	"fmt"
	"io"
	"strconv"
	"strings"

	"gozillo/internal/har"
	"gozillo/internal/output"
	gozillosession "gozillo/internal/session"
)

type sessionCommand struct{}

func (sessionCommand) Name() string    { return "session" }
func (sessionCommand) Summary() string { return "Import and manage file-backed Zillow sessions" }

func (sessionCommand) Run(ctx Context, args []string) error {
	if len(args) == 0 || wantsHelp(args) {
		writeSessionUsage(ctx.Stdout)
		return nil
	}
	switch args[0] {
	case "import":
		return runSessionImport(ctx, args[1:])
	case "list":
		return runSessionList(ctx, args[1:])
	case "inspect":
		return runSessionInspect(ctx, args[1:])
	case "remove":
		return runSessionRemove(ctx, args[1:])
	default:
		return usagef("unknown session subcommand %q", args[0])
	}
}

func runSessionImport(ctx Context, args []string) error {
	flags := flag.NewFlagSet("session import", flag.ContinueOnError)
	flags.SetOutput(ctx.Stderr)
	name := flags.String("name", "default", "session name")
	if err := flags.Parse(args); err != nil {
		return &usageError{err: err}
	}
	if flags.NArg() != 1 {
		return usagef("session import requires exactly one raw HAR file")
	}
	archive, err := har.LoadFile(flags.Arg(0))
	if err != nil {
		return err
	}
	imported, err := gozillosession.ImportHAR(archive)
	if err != nil {
		return err
	}
	store, err := gozillosession.DefaultStore()
	if err != nil {
		return err
	}
	if err := store.Save(*name, imported); err != nil {
		return err
	}
	path, _ := store.Path(*name)
	summary := imported.Summary(*name)
	_, err = fmt.Fprintf(ctx.Stdout, "imported session %q with %d cookies -> %s\n", summary.Name, summary.CookieCount, path)
	return err
}

func runSessionList(ctx Context, args []string) error {
	flags := flag.NewFlagSet("session list", flag.ContinueOnError)
	flags.SetOutput(ctx.Stderr)
	if err := flags.Parse(args); err != nil {
		return &usageError{err: err}
	}
	if flags.NArg() != 0 {
		return usagef("session list does not accept arguments")
	}
	store, err := gozillosession.DefaultStore()
	if err != nil {
		return err
	}
	summaries, err := store.List()
	if err != nil {
		return err
	}
	return printSessionSummaries(ctx, summaries)
}

func runSessionInspect(ctx Context, args []string) error {
	flags := flag.NewFlagSet("session inspect", flag.ContinueOnError)
	flags.SetOutput(ctx.Stderr)
	name := flags.String("name", "default", "session name")
	if err := flags.Parse(args); err != nil {
		return &usageError{err: err}
	}
	if flags.NArg() != 0 {
		return usagef("session inspect does not accept positional arguments")
	}
	store, err := gozillosession.DefaultStore()
	if err != nil {
		return err
	}
	loaded, err := store.Load(*name)
	if err != nil {
		return err
	}
	return printSessionSummaries(ctx, []gozillosession.Summary{loaded.Summary(*name)})
}

func runSessionRemove(ctx Context, args []string) error {
	flags := flag.NewFlagSet("session remove", flag.ContinueOnError)
	flags.SetOutput(ctx.Stderr)
	name := flags.String("name", "default", "session name")
	if err := flags.Parse(args); err != nil {
		return &usageError{err: err}
	}
	if flags.NArg() != 0 {
		return usagef("session remove does not accept positional arguments")
	}
	store, err := gozillosession.DefaultStore()
	if err != nil {
		return err
	}
	if err := store.Remove(*name); err != nil {
		return err
	}
	_, err = fmt.Fprintf(ctx.Stdout, "removed session %q\n", *name)
	return err
}

func printSessionSummaries(ctx Context, summaries []gozillosession.Summary) error {
	printer, err := output.NewPrinter(ctx.Stdout, ctx.OutputMode)
	if err != nil {
		return err
	}
	switch ctx.OutputMode {
	case output.ModeJSONL:
		for _, summary := range summaries {
			if err := printer.Print(summary); err != nil {
				return err
			}
		}
		return nil
	case output.ModeTable:
		rows := make([][]string, 0, len(summaries))
		for _, summary := range summaries {
			rows = append(rows, []string{
				summary.Name,
				summary.CreatedAt.Format("2006-01-02T15:04:05Z"),
				strconv.Itoa(summary.CookieCount),
				strings.Join(summary.CookieNames, ","),
			})
		}
		return printer.Print(output.Table{Headers: []string{"NAME", "CREATED", "COOKIES", "COOKIE NAMES"}, Rows: rows})
	default:
		return printer.Print(summaries)
	}
}

func writeSessionUsage(w io.Writer) {
	_, _ = io.WriteString(w, `Usage:
  gozillo session import --name <name> <raw.har>
  gozillo session list
  gozillo session inspect --name <name>
  gozillo session remove --name <name>

Session files are plaintext secrets stored with mode 0600 in a mode-0700
per-user config directory. Commands never print cookie values. Browser
User-Agent values and authorization headers are not imported or replayed.
`)
}
