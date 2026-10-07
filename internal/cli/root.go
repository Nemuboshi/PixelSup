package cli

import (
	"fmt"
	"io"
)

// Command describes one CLI subcommand without an external command framework.
type Command struct {
	name        string
	description string
	run         func(args []string, out io.Writer) error
}

// Name returns the command name used on CLI.
func (c *Command) Name() string {
	return c.name
}

// RootCommand manages top-level dispatch for parse/export/ocr subcommands.
type RootCommand struct {
	commands map[string]*Command
}

// BuildRootCommand constructs the root CLI command with currently supported subcommands.
func BuildRootCommand() *RootCommand {
	parseCmd := &Command{
		name:        "parse",
		description: "Parse .sup, .idx(+.sub), or numbered image dirs into sheets + timeline outputs.",
		run:         runParserCommand,
	}
	exportCmd := &Command{
		name:        "export",
		description: "Export .sup/.idx cues as per-cue PNG files with timeline outputs.",
		run:         runExportCommand,
	}
	ocrCmd := &Command{
		name:        "ocr",
		description: "Run OCR on composed sheets and backfill subtitle text.",
		run:         runOCRCommand,
	}

	return &RootCommand{
		commands: map[string]*Command{
			parseCmd.name:  parseCmd,
			exportCmd.name: exportCmd,
			ocrCmd.name:    ocrCmd,
		},
	}
}

// Commands returns all direct child subcommands.
func (r *RootCommand) Commands() []*Command {
	out := make([]*Command, 0, len(r.commands))
	if parseCmd, ok := r.commands["parse"]; ok {
		out = append(out, parseCmd)
	}
	if exportCmd, ok := r.commands["export"]; ok {
		out = append(out, exportCmd)
	}
	if ocrCmd, ok := r.commands["ocr"]; ok {
		out = append(out, ocrCmd)
	}
	return out
}

// Execute dispatches command-line arguments to a matching subcommand.
func (r *RootCommand) Execute(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		writeUsage(stdout, r)
		return 0
	}

	if args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		writeUsage(stdout, r)
		return 0
	}

	cmd, ok := r.commands[args[0]]
	if !ok {
		_, _ = fmt.Fprintf(stderr, "unknown command: %s\n\n", args[0])
		writeUsage(stderr, r)
		return 2
	}

	if err := cmd.run(args[1:], stdout); err != nil {
		if progressLineActive {
			_, _ = fmt.Fprintln(stderr)
			progressLineActive = false
		}
		_, _ = fmt.Fprintf(stderr, "command %q failed: %v\n", cmd.name, err)
		return 1
	}
	return 0
}

func writeUsage(w io.Writer, r *RootCommand) {
	_, _ = fmt.Fprintln(w, "pixelsup: subtitle tooling")
	_, _ = fmt.Fprintln(w, "")
	_, _ = fmt.Fprintln(w, "Usage:")
	_, _ = fmt.Fprintln(w, "  pixelsup <command> [args]")
	_, _ = fmt.Fprintln(w, "")
	_, _ = fmt.Fprintln(w, "Commands:")
	for _, cmd := range r.Commands() {
		_, _ = fmt.Fprintf(w, "  %-8s %s\n", cmd.name, cmd.description)
	}
}
