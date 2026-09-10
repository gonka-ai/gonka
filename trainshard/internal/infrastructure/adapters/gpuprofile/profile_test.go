package gpuprofile_test

import (
	"testing"

	"github.com/productscience/inference/x/inference/types"

	"trainshard/internal/infrastructure/adapters/gpuprofile"
)

func TestFromCardsMatchesWhatTheDapiPutsOnChain(t *testing.T) {
	cards := []gpuprofile.Card{
		{Name: "NVIDIA H100 80GB HBM3", MemoryMiB: 81559},
		{Name: "NVIDIA H100 80GB HBM3", MemoryMiB: 81559},
		{Name: "Tesla T4", MemoryMiB: 15360},
	}
	declared := []*types.Hardware{
		{Type: "NVIDIA H100 80GB HBM3 | 79GB", Count: 2},
		{Type: "Tesla T4 | 15GB", Count: 1},
		{Type: "CPU", Count: 64},
	}

	machine := gpuprofile.FromCards(cards)
	chain := gpuprofile.FromHardware(declared)

	if machine.Profile != chain.Profile {
		t.Fatalf("machine %q, chain %q", machine.Profile, chain.Profile)
	}
	if machine.Profile != "NVIDIA H100 80GB HBM3 | 79GB x2 | TESLA T4 | 15GB x1" {
		t.Fatalf("got %q", machine.Profile)
	}
	if machine.Count != 3 {
		t.Fatalf("got %d cards", machine.Count)
	}
}

func TestNoCardsIsNoProfile(t *testing.T) {
	if got := gpuprofile.FromCards(nil); !got.IsZero() || got.Count != 0 {
		t.Fatalf("got %+v", got)
	}
	if got := gpuprofile.Declared("H100", 0); !got.IsZero() {
		t.Fatalf("got %+v", got)
	}
	if got := gpuprofile.Declared("H100", 8); got.Profile != "H100 x8" || got.Count != 8 {
		t.Fatalf("got %+v", got)
	}
}
