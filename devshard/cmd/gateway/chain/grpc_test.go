package chain

import (
	"context"
	"testing"
)

// Test flow:
//  1. Create a GRPCChain configured with a padded chain id and no gRPC connection.
//  2. Call ChainID.
//  3. Assert it returns the configured id trimmed, without asking the node.
func TestAConfiguredChainIDIsUsedWithoutAskingTheNode(t *testing.T) {
	grpcChain := NewGRPCChain(nil, "  gonka-mainnet  ")

	chainID, err := grpcChain.ChainID(context.Background())
	if err != nil {
		t.Fatalf("ChainID: %v", err)
	}
	if chainID != "gonka-mainnet" {
		t.Fatalf("chain id = %q, want the configured one", chainID)
	}
}

// Test flow:
//  1. For each table case's governance model_args (the flag and value as separate args, joined by '=', absent, valueless, non-numeric, overflowing, and repeated), call `maxModelLenOf`.
//  2. Assert it returns the flag's value, or 0 whenever the args carry no plain number for it.
func TestMaxModelLenIsReadFromTheModelsVLLMArguments(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		args []string
		want uint64
	}{
		{name: "flag_and_value_as_separate_args", args: []string{"--tensor-parallel-size", "8", "--max-model-len", "180000"}, want: 180000},
		{name: "flag_joined_to_its_value", args: []string{"--max-model-len=400000", "--enable-auto-tool-choice"}, want: 400000},
		{name: "flag_absent", args: []string{"--tensor-parallel-size", "8"}},
		{name: "flag_without_a_value", args: []string{"--max-model-len"}},
		{name: "flag_followed_by_another_flag", args: []string{"--max-model-len", "--enable-auto-tool-choice"}},
		{name: "non_numeric_value", args: []string{"--max-model-len", "auto"}},
		{name: "overflowing_value", args: []string{"--max-model-len=99999999999999999999999"}},
		{name: "a_longer_flag_sharing_the_prefix", args: []string{"--max-model-len-override", "5"}},
		{name: "no_args"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			if got := maxModelLenOf(testCase.args); got != testCase.want {
				t.Fatalf("maxModelLenOf(%q) = %d, want %d", testCase.args, got, testCase.want)
			}
		})
	}
}
