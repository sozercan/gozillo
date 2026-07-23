// Package cli contains gozillo command-line parsing and dispatch.
package cli

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"sort"
	"strings"

	"gozillo/internal/output"
)

const (
	// Name is the executable name used in help and error output.
	Name = "gozillo"

	// Exit codes returned by Execute.
	ExitOK      = 0
	ExitFailure = 1
	ExitUsage   = 2
)

// Context contains process-level dependencies shared by subcommands.
// Commands should write normal results to Stdout and diagnostics to Stderr.
type Context struct {
	Stdout     io.Writer
	Stderr     io.Writer
	OutputMode output.Mode
}

// Command is the extension point for a gozillo subcommand.
type Command interface {
	Name() string
	Summary() string
	Run(Context, []string) error
}

// Root parses global flags and dispatches registered subcommands.
type Root struct {
	stdout   io.Writer
	stderr   io.Writer
	commands map[string]Command
}

type commandHelp struct {
	name    string
	summary string
}

var plannedCommands = []commandHelp{
	{name: "search", summary: "Search Zillow through pure Go HTTP or a saved snapshot"},
	{name: "property", summary: "Extract normalized Zillow property details"},
	{name: "har", summary: "Capture, sanitize, and derive Zillow HAR data"},
	{name: "session", summary: "Import and manage file-backed Zillow sessions"},
	{name: "version", summary: "Print the gozillo version"},
}

type usageError struct {
	err error
}

func (e *usageError) Error() string {
	return e.err.Error()
}

func (e *usageError) Unwrap() error {
	return e.err
}

// NewRoot creates a root command with an optional set of subcommands.
func NewRoot(stdout, stderr io.Writer, commands ...Command) (*Root, error) {
	r := &Root{
		stdout:   writerOrDiscard(stdout),
		stderr:   writerOrDiscard(stderr),
		commands: make(map[string]Command, len(commands)),
	}

	for _, command := range commands {
		if err := r.Register(command); err != nil {
			return nil, err
		}
	}

	return r, nil
}

// Register adds a command to the dispatch table.
func (r *Root) Register(command Command) error {
	if command == nil {
		return errors.New("register command: command is nil")
	}

	name := command.Name()
	if !validCommandName(name) {
		return fmt.Errorf("register command: invalid name %q", name)
	}
	if _, exists := r.commands[name]; exists {
		return fmt.Errorf("register command: duplicate name %q", name)
	}

	r.commands[name] = command
	return nil
}

// Run parses args and invokes a registered command.
func (r *Root) Run(args []string) error {
	flags := flag.NewFlagSet(Name, flag.ContinueOnError)
	flags.SetOutput(io.Discard)

	modeValue := output.ModeTable.String()
	showHelp := false
	flags.StringVar(&modeValue, "output", modeValue, "output mode")
	flags.StringVar(&modeValue, "o", modeValue, "output mode")
	flags.BoolVar(&showHelp, "help", false, "show help")
	flags.BoolVar(&showHelp, "h", false, "show help")

	if err := flags.Parse(args); err != nil {
		return &usageError{err: err}
	}

	remaining := flags.Args()
	if showHelp || len(remaining) == 0 || remaining[0] == "help" {
		return r.WriteUsage(r.stdout)
	}

	mode, err := output.ParseMode(modeValue)
	if err != nil {
		return &usageError{err: err}
	}

	name := remaining[0]
	command, exists := r.commands[name]
	if !exists {
		if isPlannedCommand(name) {
			return &usageError{err: fmt.Errorf("command %q is not implemented yet", name)}
		}
		return &usageError{err: fmt.Errorf("unknown command %q", name)}
	}

	ctx := Context{
		Stdout:     r.stdout,
		Stderr:     r.stderr,
		OutputMode: mode,
	}
	if err := command.Run(ctx, remaining[1:]); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}

	return nil
}

// WriteUsage writes deterministic root help text.
func (r *Root) WriteUsage(w io.Writer) error {
	commands := r.commandHelp()
	width := 0
	for _, command := range commands {
		if len(command.name) > width {
			width = len(command.name)
		}
	}

	var help strings.Builder
	help.WriteString("Usage:\n")
	help.WriteString("  gozillo [global options] <command> [arguments]\n\n")
	help.WriteString("A pure-Go Zillow web client.\n\n")
	help.WriteString("Global options:\n")
	help.WriteString("  -o, --output <mode>  Output mode: table, json, or jsonl (default: table)\n")
	help.WriteString("  -h, --help           Show this help\n\n")
	help.WriteString("Commands:\n")
	for _, command := range commands {
		fmt.Fprintf(&help, "  %-*s  %s\n", width, command.name, command.summary)
	}

	_, err := io.WriteString(writerOrDiscard(w), help.String())
	return err
}

func (r *Root) commandHelp() []commandHelp {
	commands := make([]commandHelp, 0, len(plannedCommands)+len(r.commands))
	planned := make(map[string]struct{}, len(plannedCommands))

	for _, entry := range plannedCommands {
		planned[entry.name] = struct{}{}
		if command, exists := r.commands[entry.name]; exists {
			commands = append(commands, commandHelp{
				name:    entry.name,
				summary: commandSummary(command),
			})
			continue
		}
		commands = append(commands, commandHelp{
			name:    entry.name,
			summary: entry.summary + " (not implemented)",
		})
	}

	extraNames := make([]string, 0, len(r.commands))
	for name := range r.commands {
		if _, exists := planned[name]; !exists {
			extraNames = append(extraNames, name)
		}
	}
	sort.Strings(extraNames)
	for _, name := range extraNames {
		commands = append(commands, commandHelp{
			name:    name,
			summary: commandSummary(r.commands[name]),
		})
	}

	return commands
}

func commandSummary(command Command) string {
	summary := strings.TrimSpace(command.Summary())
	if summary == "" {
		return "No description"
	}
	return summary
}

func isPlannedCommand(name string) bool {
	for _, command := range plannedCommands {
		if command.name == name {
			return true
		}
	}
	return false
}

func validCommandName(name string) bool {
	if name == "" || name == "help" {
		return false
	}
	for index, char := range name {
		if char >= 'a' && char <= 'z' {
			continue
		}
		if index > 0 && (char == '-' || char >= '0' && char <= '9') {
			continue
		}
		return false
	}
	return true
}

func writerOrDiscard(w io.Writer) io.Writer {
	if w == nil {
		return io.Discard
	}
	return w
}

// Execute runs the CLI and converts errors into process exit codes.
func Execute(args []string, stdout, stderr io.Writer, commands ...Command) int {
	stdout = writerOrDiscard(stdout)
	stderr = writerOrDiscard(stderr)

	if len(commands) == 0 {
		commands = DefaultCommands()
	}

	root, err := NewRoot(stdout, stderr, commands...)
	if err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", Name, err)
		return ExitFailure
	}

	if err := root.Run(args); err != nil {
		fmt.Fprintf(stderr, "%s: %v\n", Name, err)

		var usage *usageError
		if errors.As(err, &usage) {
			return ExitUsage
		}
		return ExitFailure
	}

	return ExitOK
}
