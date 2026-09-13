package types

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDevshardMaxModelLenForCreate(t *testing.T) {
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
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			require.Equal(t, testCase.maxModelLen, DevshardMaxModelLenForCreate(testCase.model))
		})
	}
}
