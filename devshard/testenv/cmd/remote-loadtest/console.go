package main

import (
	"fmt"
	"io"
	"strings"

	"devshard/testenv/loadtest"
)

func consoleSection(out io.Writer, title string) {
	line := strings.Repeat("*", 72)
	fmt.Fprintf(out, "\n%s\n  %s\n%s\n\n", line, title, line)
}

func consoleSummary(out io.Writer, verdict string, models *loadtest.ModelBreakdown) {
	consoleSection(out, "REPORT SUMMARY")
	fmt.Fprintf(out, "Result: %s\n\n", verdict)
	// The section banner already supplies the title for this standalone table.
	fmt.Fprint(out, strings.TrimPrefix(loadtest.RenderModelBreakdownText(models), "\nMODEL SUMMARY\n\n"))
}
