package clix

import (
	"flag"
	"fmt"
	"io"
	"slices"
	"strings"

	"trainshard/internal/domain/shared"
)

func Asked(arg string) bool { return arg == "-h" || arg == "--help" }

// Wants looks for help only before "--": what follows is the container's own command
func Wants(args []string) bool {
	if end := slices.Index(args, "--"); end >= 0 {
		args = args[:end]
	}
	return slices.ContainsFunc(args, Asked)
}

// Command is a flag set whose help says what the command does and how it is called, so a command
// without flags does not answer with a bare usage line
func Command(use, summary string, examples ...string) *flag.FlagSet {
	flags := flag.NewFlagSet(use, flag.ContinueOnError)
	flags.Usage = func() {
		out := flags.Output()
		fmt.Fprintf(out, "usage: %s\n\n%s\n", use, summary)
		if len(examples) > 0 {
			fmt.Fprintln(out, "\nexamples:")
			for _, example := range examples {
				fmt.Fprintf(out, "  %s\n", example)
			}
		}
		hasFlags := false
		flags.VisitAll(func(*flag.Flag) { hasFlags = true })
		if hasFlags {
			fmt.Fprintln(out, "\nflags:")
			flags.PrintDefaults()
		}
	}
	return flags
}

func Parse(flags *flag.FlagSet, args []string, targets ...string) ([]string, error) {
	if Wants(args) {
		flags.Usage()
		return nil, flag.ErrHelp
	}
	if len(args) < len(targets) {
		flags.Usage()
		return nil, fmt.Errorf("%s: %w", strings.Join(targets, " and "), shared.ErrValidation)
	}

	out := flags.Output()
	flags.SetOutput(io.Discard)
	err := flags.Parse(args[len(targets):])
	flags.SetOutput(out)
	if err != nil {
		flags.Usage()
		return nil, err
	}
	return args[:len(targets)], nil
}
