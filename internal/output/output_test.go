package output

import (
	"bytes"
	"fmt"
	"io"
	"testing"
)

func TestParseMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  Mode
	}{
		{input: "table", want: ModeTable},
		{input: "json", want: ModeJSON},
		{input: "jsonl", want: ModeJSONL},
		{input: " JSONL ", want: ModeJSONL},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			t.Parallel()

			got, err := ParseMode(tt.input)
			if err != nil {
				t.Fatalf("ParseMode(%q) error = %v", tt.input, err)
			}
			if got != tt.want {
				t.Fatalf("ParseMode(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestParseModeRejectsUnknownMode(t *testing.T) {
	t.Parallel()

	_, err := ParseMode("yaml")
	if err == nil {
		t.Fatal("ParseMode() error = nil, want validation error")
	}
	if err.Error() != `unknown output mode "yaml" (want table, json, or jsonl)` {
		t.Fatalf("ParseMode() error = %q, want deterministic validation error", err)
	}
}

func TestPrinterJSON(t *testing.T) {
	t.Parallel()

	type listing struct {
		Address string `json:"address"`
		Price   int    `json:"price"`
	}

	var output bytes.Buffer
	printer, err := NewPrinter(&output, ModeJSON)
	if err != nil {
		t.Fatalf("NewPrinter() error = %v", err)
	}
	if err := printer.Print(listing{Address: "1 Main St", Price: 450000}); err != nil {
		t.Fatalf("Print() error = %v", err)
	}

	const want = `{
  "address": "1 Main St",
  "price": 450000
}
`
	if output.String() != want {
		t.Fatalf("Print() output = %q, want %q", output.String(), want)
	}
}

func TestPrinterJSONLStreamsOneRecordPerCall(t *testing.T) {
	t.Parallel()

	type record struct {
		ID int `json:"id"`
	}

	var output bytes.Buffer
	printer, err := NewPrinter(&output, ModeJSONL)
	if err != nil {
		t.Fatalf("NewPrinter() error = %v", err)
	}
	for _, value := range []record{{ID: 1}, {ID: 2}} {
		if err := printer.Print(value); err != nil {
			t.Fatalf("Print() error = %v", err)
		}
	}

	const want = "{\"id\":1}\n{\"id\":2}\n"
	if output.String() != want {
		t.Fatalf("Print() output = %q, want %q", output.String(), want)
	}
}

func TestPrinterTable(t *testing.T) {
	t.Parallel()

	table := Table{
		Headers: []string{"ADDRESS", "PRICE"},
		Rows: [][]string{
			{"1 Main", "$450,000"},
			{"22 Oak", "$525,000"},
		},
	}

	var output bytes.Buffer
	printer, err := NewPrinter(&output, ModeTable)
	if err != nil {
		t.Fatalf("NewPrinter() error = %v", err)
	}
	if err := printer.Print(table); err != nil {
		t.Fatalf("Print() error = %v", err)
	}

	const want = "ADDRESS  PRICE\n1 Main   $450,000\n22 Oak   $525,000\n"
	if output.String() != want {
		t.Fatalf("Print() output = %q, want %q", output.String(), want)
	}
}

func TestPrinterRejectsRaggedTableBeforeWriting(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	printer, err := NewPrinter(&output, ModeTable)
	if err != nil {
		t.Fatalf("NewPrinter() error = %v", err)
	}

	err = printer.Print(Table{
		Headers: []string{"A", "B"},
		Rows:    [][]string{{"only one"}},
	})
	if err == nil {
		t.Fatal("Print() error = nil, want table validation error")
	}
	if err.Error() != "table row 1 has 1 cells; want 2" {
		t.Fatalf("Print() error = %q, want deterministic validation error", err)
	}
	if output.String() != "" {
		t.Fatalf("Print() output = %q, want no partial output", output.String())
	}
}

func TestPrinterRejectsTerminalControlCharacters(t *testing.T) {
	t.Parallel()

	for _, value := range []string{
		"escape\x1b[31m",
		"bell\a",
		"form-feed\f",
		"c1\u009b31m",
	} {
		value := value
		t.Run(fmt.Sprintf("%q", value), func(t *testing.T) {
			t.Parallel()
			var output bytes.Buffer
			printer, err := NewPrinter(&output, ModeTable)
			if err != nil {
				t.Fatal(err)
			}
			err = printer.Print(Table{Headers: []string{"VALUE"}, Rows: [][]string{{value}}})
			if err == nil {
				t.Fatalf("Print(%q) error = nil", value)
			}
			if output.Len() != 0 {
				t.Fatalf("Print(%q) wrote partial output %q", value, output.String())
			}
		})
	}
}

func TestNewPrinterRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		writer io.Writer
		mode   Mode
		want   string
	}{
		{name: "nil writer", writer: nil, mode: ModeJSON, want: "output writer is nil"},
		{name: "invalid mode", writer: &bytes.Buffer{}, mode: Mode("yaml"), want: `unsupported output mode "yaml"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := NewPrinter(tt.writer, tt.mode)
			if err == nil {
				t.Fatal("NewPrinter() error = nil, want configuration error")
			}
			if err.Error() != tt.want {
				t.Fatalf("NewPrinter() error = %q, want %q", err, tt.want)
			}
		})
	}
}
