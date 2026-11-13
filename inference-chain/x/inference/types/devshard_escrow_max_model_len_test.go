package types

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDevshardMaxModelLenForCreate(t *testing.T) {
	// Test flow:
	// 1. Build a model snapshot whose vLLM args spell --max-model-len one way per case.
	// 2. Read the cap the escrow would freeze at create.
	// 3. Expect the value vLLM itself would run with, or 0 (uncapped) when it cannot be read.
	cases := []struct {
		name        string
		model       *Model
		maxModelLen uint64
	}{
		{name: "flag and value as separate arguments", model: &Model{ModelArgs: []string{"--tool-call-parser", "glm47", "--max-model-len", "400000"}}, maxModelLen: 400_000},
		{name: "flag joined to its value", model: &Model{ModelArgs: []string{"--max-model-len=180000"}}, maxModelLen: 180_000},
		{name: "flag absent", model: &Model{ModelArgs: []string{"--enable-auto-tool-choice"}}, maxModelLen: 0},
		{name: "value is not a plain number", model: &Model{ModelArgs: []string{"--max-model-len", "400k"}}, maxModelLen: 0},
		{name: "flag without a value", model: &Model{ModelArgs: []string{"--max-model-len"}}, maxModelLen: 0},
		{name: "no model snapshot", model: nil, maxModelLen: 0},
		{name: "underscore spelling as separate arguments", model: &Model{ModelArgs: []string{"--max_model_len", "240000"}}, maxModelLen: 240_000},
		{name: "underscore spelling joined to its value", model: &Model{ModelArgs: []string{"--max_model-len=240000"}}, maxModelLen: 240_000},
		{name: "repeated flag takes the last value", model: &Model{ModelArgs: []string{"--max-model-len", "8192", "--max-model-len=240000"}}, maxModelLen: 240_000},
		{name: "repeated flag across spellings takes the last value", model: &Model{ModelArgs: []string{"--max_model_len=240000", "--max-model-len", "8192"}}, maxModelLen: 8192},
		{name: "unreadable last value leaves the cap off", model: &Model{ModelArgs: []string{"--max-model-len", "8192", "--max-model-len", "400k"}}, maxModelLen: 0},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			require.Equal(t, testCase.maxModelLen, DevshardMaxModelLenForCreate(testCase.model))
		})
	}
}
