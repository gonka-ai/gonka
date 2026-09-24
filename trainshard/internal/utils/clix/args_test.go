package clix_test

import (
	"errors"
	"flag"
	"strings"
	"testing"

	"trainshard/internal/utils/clix"
)

func TestHelpForACommandWithoutFlagsSaysWhatItDoes(t *testing.T) {
	// arrange
	flags := clix.Command("settle <shard>", "Closes the shard on the chain.")
	var out strings.Builder
	flags.SetOutput(&out)

	// act
	_, err := clix.Parse(flags, []string{"--help"}, "shard")

	// assert
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("got %v, want help", err)
	}
	if got := out.String(); got != "usage: settle <shard>\n\nCloses the shard on the chain.\n" {
		t.Fatalf("got %q, want the usage and the summary with nothing left hanging", got)
	}
}

func TestHelpForACommandWithFlagsListsItsExamplesThenItsFlags(t *testing.T) {
	// arrange
	flags := clix.Command("stop <shard> [flags]", "Stops the run.", "trainshardctl stop 1 -grace 2m")
	flags.Duration("grace", 0, "how long a container may take to exit")
	var out strings.Builder
	flags.SetOutput(&out)

	// act
	_, _ = clix.Parse(flags, []string{"--help"}, "shard")

	// assert
	got := out.String()
	want := "Stops the run.\n\nexamples:\n  trainshardctl stop 1 -grace 2m\n\nflags:\n"
	if !strings.Contains(got, want) || !strings.Contains(got, "-grace") {
		t.Fatalf("got %q, want the summary, the examples and then the flags", got)
	}
}
