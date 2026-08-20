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
