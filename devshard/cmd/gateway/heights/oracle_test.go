package heights

import (
	"testing"

	"devshard/cmd/gateway/config"
)

// Test flow:
//  1. Call NewOracle with height sync enabled but the oracle flag off, and a Comet RPC source set.
//  2. Assert it returns no error.
//  3. Assert the returned oracle is nil.
func TestNoFollowerUnlessItIsAskedFor(t *testing.T) {
	oracle, err := NewOracle(config.HeightSync{Enabled: true}, OracleSources{CometRPC: "http://127.0.0.1:26657"})

	if err != nil {
		t.Fatalf("NewOracle() = %v", err)
	}
	if oracle != nil {
		t.Fatal("NewOracle() built a follower nobody asked for")
	}
}

// Test flow:
//  1. Call NewOracle with height sync off but the oracle flag on, and a Comet RPC source set.
//  2. Assert it returns no error.
//  3. Assert the returned oracle is nil.
func TestNoFollowerWhileHeightSyncIsOff(t *testing.T) {
	oracle, err := NewOracle(config.HeightSync{ChainOracle: true}, OracleSources{CometRPC: "http://127.0.0.1:26657"})

	if err != nil {
		t.Fatalf("NewOracle() = %v", err)
	}
	if oracle != nil {
		t.Fatal("NewOracle() built a follower for a gateway that carries no heights")
	}
}

// Test flow:
//  1. Call NewOracle with height sync and the oracle flag both enabled, but no source set.
//  2. Assert it returns no error.
//  3. Assert the returned oracle is nil.
func TestNoFollowerWithoutASource(t *testing.T) {
	oracle, err := NewOracle(config.HeightSync{Enabled: true, ChainOracle: true}, OracleSources{})

	if err != nil {
		t.Fatalf("NewOracle() = %v", err)
	}
	if oracle != nil {
		t.Fatal("NewOracle() built a follower with no source behind it")
	}
}

// Test flow:
//  1. Build an oracle with height sync and the oracle flag enabled, and a named Comet endpoint.
//  2. Assert it builds successfully and returns a non-nil oracle.
//  3. Call Close on it twice.
//  4. Assert both calls return nil.
func TestClosingTheFollowerTwiceIsSafe(t *testing.T) {
	oracle, err := NewOracle(config.HeightSync{Enabled: true, ChainOracle: true},
		OracleSources{CometRPC: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatalf("NewOracle() = %v", err)
	}
	if oracle == nil {
		t.Fatal("NewOracle() built nothing for a named Comet endpoint")
	}

	if err := oracle.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
	if err := oracle.Close(); err != nil {
		t.Fatalf("second Close() = %v", err)
	}
}

// Test flow:
//  1. Hold a nil *Oracle.
//  2. Call Close on it.
//  3. Assert it returns nil.
func TestClosingNothingIsFine(t *testing.T) {
	var oracle *Oracle

	if err := oracle.Close(); err != nil {
		t.Fatalf("Close() = %v", err)
	}
}

// Test flow:
//  1. Hold a nil *Oracle and build a courier with height sync enabled, passing that nil oracle.
//  2. Assert the courier is non-nil.
//  3. Assert the courier's HeightSyncLogOracle field is nil.
func TestACourierWithoutAFollowerHoldsNoOracle(t *testing.T) {
	var absent *Oracle

	courier := BuildCourier(config.HeightSync{Enabled: true}, absent)

	if courier == nil {
		t.Fatal("BuildCourier() = nil for an enabled height sync")
	}
	if courier.HeightSyncLogOracle != nil {
		t.Fatal("the courier holds a follower that does not exist")
	}
}

// Test flow:
//  1. Build an oracle with height sync and the oracle flag enabled, and a named Comet endpoint.
//  2. Build a courier with height sync enabled, passing that oracle.
//  3. Assert the courier's HeightSyncLogOracle field is not nil.
func TestACourierCarriesTheFollowerItWasGiven(t *testing.T) {
	oracle, err := NewOracle(config.HeightSync{Enabled: true, ChainOracle: true},
		OracleSources{CometRPC: "http://127.0.0.1:1"})
	if err != nil || oracle == nil {
		t.Fatalf("NewOracle() = %v, %v", oracle, err)
	}
	t.Cleanup(func() { _ = oracle.Close() })

	courier := BuildCourier(config.HeightSync{Enabled: true}, oracle)

	if courier.HeightSyncLogOracle == nil {
		t.Fatal("the courier dropped the follower it was given")
	}
}
