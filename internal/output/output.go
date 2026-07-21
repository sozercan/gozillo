// Package output formats command results for terminal and machine consumers.
package output

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"unicode"
)

// Mode identifies a supported output format.
type Mode string

const (
	ModeTable Mode = "table"
	ModeJSON  Mode = "json"
	ModeJSONL Mode = "jsonl"
)

// ParseMode validates and canonicalizes an output mode.
func ParseMode(value string) (Mode, error) {
	mode := Mode(strings.ToLower(strings.TrimSpace(value)))
	if !mode.Valid() {
		return "", fmt.Errorf(
			"unknown output mode %q (want table, json, or jsonl)",
			value,
		)
	}
	return mode, nil
}

// Valid reports whether mode is supported.
func (mode Mode) Valid() bool {
	switch mode {
	case ModeTable, ModeJSON, ModeJSONL:
		return true
	default:
		return false
	}
}

// String returns the command-line spelling of mode.
func (mode Mode) String() string {
	return string(mode)
}

// Table is the structured value accepted by Printer in table mode.
type Table struct {
	Headers []string
	Rows    [][]string
}

// Tabular can be implemented by result types that know their table view.
type Tabular interface {
	Table() Table
}

// Printer writes values in one configured output mode.
type Printer struct {
	writer io.Writer
	mode   Mode
}

// NewPrinter creates a Printer for writer and mode.
func NewPrinter(writer io.Writer, mode Mode) (*Printer, error) {
	if writer == nil {
		return nil, errors.New("output writer is nil")
	}
	if !mode.Valid() {
		return nil, fmt.Errorf("unsupported output mode %q", mode)
	}
	return &Printer{writer: writer, mode: mode}, nil
}

// Mode returns the Printer's configured output mode.
func (printer *Printer) Mode() Mode {
	if printer == nil {
		return ""
	}
	return printer.mode
}

// Print writes value once. In JSONL mode, each call writes one JSON value and
// a trailing newline, so callers can stream records without buffering a slice.
func (printer *Printer) Print(value any) error {
	if printer == nil {
		return errors.New("output printer is nil")
	}

	switch printer.mode {
	case ModeJSON:
		encoder := json.NewEncoder(printer.writer)
		encoder.SetEscapeHTML(false)
		encoder.SetIndent("", "  ")
		return encoder.Encode(value)
	case ModeJSONL:
		encoder := json.NewEncoder(printer.writer)
		encoder.SetEscapeHTML(false)
		return encoder.Encode(value)
	case ModeTable:
		table, err := tableFrom(value)
		if err != nil {
			return err
		}
		return writeTable(printer.writer, table)
	default:
		return fmt.Errorf("unsupported output mode %q", printer.mode)
	}
}

func tableFrom(value any) (Table, error) {
	switch value := value.(type) {
	case Table:
		return value, nil
	case *Table:
		if value == nil {
			return Table{}, errors.New("table value is nil")
		}
		return *value, nil
	case Tabular:
		return value.Table(), nil
	default:
		return Table{}, fmt.Errorf("table mode requires output.Table or output.Tabular, got %T", value)
	}
}

func writeTable(writer io.Writer, table Table) error {
	if err := validateTable(table); err != nil {
		return err
	}
	if len(table.Headers) == 0 && len(table.Rows) == 0 {
		return nil
	}

	tw := tabwriter.NewWriter(writer, 0, 4, 2, ' ', 0)
	if len(table.Headers) > 0 {
		if _, err := fmt.Fprintln(tw, strings.Join(table.Headers, "\t")); err != nil {
			return err
		}
	}
	for _, row := range table.Rows {
		if _, err := fmt.Fprintln(tw, strings.Join(row, "\t")); err != nil {
			return err
		}
	}
	return tw.Flush()
}

func validateTable(table Table) error {
	columns := -1
	if len(table.Headers) > 0 {
		columns = len(table.Headers)
		for index, cell := range table.Headers {
			if err := validateCell(cell); err != nil {
				return fmt.Errorf("table header cell %d: %w", index+1, err)
			}
		}
	}

	for rowIndex, row := range table.Rows {
		if columns < 0 {
			columns = len(row)
		}
		if len(row) != columns {
			return fmt.Errorf(
				"table row %d has %d cells; want %d",
				rowIndex+1,
				len(row),
				columns,
			)
		}
		for cellIndex, cell := range row {
			if err := validateCell(cell); err != nil {
				return fmt.Errorf(
					"table row %d cell %d: %w",
					rowIndex+1,
					cellIndex+1,
					err,
				)
			}
		}
	}

	return nil
}

func validateCell(value string) error {
	for _, character := range value {
		if unicode.IsControl(character) {
			return fmt.Errorf("contains control character U+%04X", character)
		}
	}
	return nil
}
