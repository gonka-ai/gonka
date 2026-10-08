package host

import (
	"testing"
)

func TestShouldPersistSnapshot(t *testing.T) {
	t.Setenv(DisablePeriodicSnapshotsEnv, "")
	if !ShouldPersistSnapshot(SnapshotInterval, false) {
		t.Fatal("interval nonce should snapshot when periodic snapshots are enabled")
	}
	if ShouldPersistSnapshot(1, false) {
		t.Fatal("nonce 1 should not snapshot")
	}
	if !ShouldPersistSnapshot(1, true) {
		t.Fatal("entering settlement should snapshot")
	}

	t.Setenv(DisablePeriodicSnapshotsEnv, "true")
	if ShouldPersistSnapshot(SnapshotInterval, false) {
		t.Fatal("interval nonce should not snapshot when periodic snapshots are disabled")
	}
	if !ShouldPersistSnapshot(SnapshotInterval, true) {
		t.Fatal("entering settlement should snapshot when periodic snapshots are disabled")
	}
	if ShouldPersistSnapshot(0, true) {
		t.Fatal("nonce 0 should not snapshot")
	}

	t.Setenv(DisablePeriodicSnapshotsEnv, "nope")
	if !ShouldPersistSnapshot(SnapshotInterval, false) {
		t.Fatal("an unparseable value should keep periodic snapshots")
	}
}
