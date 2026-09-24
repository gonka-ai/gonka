package clix

import (
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	"trainshard/internal/domain/shared"
)

func Asked(arg string) bool { return arg == "-h" || arg == "--help" }

// Command is a flag set whose help says what the command does, so a command without flags does
// not answer with a bare usage line
func Command(use, summary string) *flag.FlagSet {
	flags := flag.NewFlagSet(use, flag.ContinueOnError)
	flags.Usage = func() {
		out := flags.Output()
		fmt.Fprintf(out, "usage: %s\n\n%s\n", use, summary)
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
	if slices.ContainsFunc(args, Asked) {
		flags.Usage()
		return nil, flag.ErrHelp
	}
	if len(args) < len(targets) {
		flags.Usage()
		return nil, fmt.Errorf("%s: %w", strings.Join(targets, " and "), shared.ErrValidation)
	}

	flags.SetOutput(io.Discard)
	err := flags.Parse(args[len(targets):])
	flags.SetOutput(os.Stderr)
	if err != nil {
		flags.Usage()
		return nil, err
	}
	return args[:len(targets)], nil
}
