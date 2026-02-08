package main

import (
	"os"

	"github.com/fatih/color"
	"github.com/jedib0t/go-pretty/v6/table"
	"github.com/jedib0t/go-pretty/v6/text"
)

// Color variables for consistent styling across all commands.
var (
	colorHeader       = color.New(color.FgWhite, color.Bold)
	colorSuccess      = color.New(color.FgGreen)
	colorError        = color.New(color.FgRed)
	colorWarning      = color.New(color.FgYellow)
	colorMuted        = color.New(color.Faint)
	colorProtocolNFS  = color.New(color.FgBlue)
	colorProtocolNVMe = color.New(color.FgMagenta)
	colorProtocolISCI = color.New(color.FgYellow)
)

// protocolBadge returns a colored protocol name.
func protocolBadge(protocol string) string {
	switch protocol {
	case protocolNFS:
		return colorProtocolNFS.Sprint("NFS")
	case protocolNVMeOF:
		return colorProtocolNVMe.Sprint("NVMe-oF")
	case protocolISCSI:
		return colorProtocolISCI.Sprint("iSCSI")
	default:
		if protocol == "" {
			return colorMuted.Sprint("-")
		}
		return protocol
	}
}

// newStyledTable creates a pre-configured go-pretty table with StyleLight base,
// bold white headers, and no row separators.
func newStyledTable() table.Writer {
	t := table.NewWriter()
	t.SetOutputMirror(os.Stdout)

	style := table.StyleLight
	style.Options.SeparateRows = false
	style.Options.DrawBorder = false
	style.Options.SeparateColumns = true
	style.Format.Header = text.FormatUpper
	style.Format.HeaderAlign = text.AlignLeft
	t.SetStyle(style)

	return t
}

// renderTable renders the table to stdout.
func renderTable(t table.Writer) {
	t.Render()
}
