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

func TestAHelpFlagAfterTheSeparatorBelongsToTheContainersCommand(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want []string
	}{
		{"short help", []string{"1", "-gpus", "1", "--", "train.py", "-h"}, []string{"train.py", "-h"}},
		{"long help", []string{"1", "--", "train.py", "--help"}, []string{"train.py", "--help"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// arrange
			flags := clix.Command("deploy <shard> [flags] [-- command]", "Places the run.")
			flags.Int("gpus", 0, "gpus per node")

			// act
			rest, err := clix.Parse(flags, tc.args, "shard")

			// assert
			if err != nil {
				t.Fatalf("got %v, want the command parsed", err)
			}
			if len(rest) != 1 || rest[0] != "1" || strings.Join(flags.Args(), " ") != strings.Join(tc.want, " ") {
				t.Fatalf("got %v and %v, want the shard and %v", rest, flags.Args(), tc.want)
			}
		})
	}
}

func TestAFlagThatDoesNotParseIsAnsweredWhereTheCallerAsked(t *testing.T) {
	// arrange
	flags := clix.Command("stop <shard> [flags]", "Stops the run.")
	var out strings.Builder
	flags.SetOutput(&out)

	// act
	_, err := clix.Parse(flags, []string{"1", "-nope"}, "shard")

	// assert
	if err == nil {
		t.Fatal("want an unknown flag refused")
	}
	if !strings.Contains(out.String(), "usage: stop <shard> [flags]") {
		t.Fatalf("got %q, want the usage on the caller's own output", out.String())
	}
}
