package cli

import (
	"encoding/json"
	"io"
	"text/tabwriter"
)

// printJSON writes v as indented JSON — the -o json contract (AGENTS/
// 09-cli-and-tui.md: "-o json is a contract; the table is not").
func printJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// newTable returns a tabwriter configured the same way for every verb's
// table output, so column alignment is consistent across the CLI.
func newTable(w io.Writer) *tabwriter.Writer {
	return tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
}
