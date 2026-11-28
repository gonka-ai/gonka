package storetest

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"devshard/cmd/gateway/store"
)

// Test flow:
//  1. Open a store, insert two keys, and revoke the first.
//  2. Close the store and open a new one on the same database.
//  3. Assert the list returns both rows oldest first with the revoked time on the first and none on the second, `APIKey` returns a row by ID and reports an unknown ID as not found, and `ActiveAPIKeys` holds only the second key.
func anAPIKeySurvivesAReopenWithItsRevokedState(t *testing.T, open Opener) {
	ctx := context.Background()
	createdAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	revokedAt := createdAt.Add(time.Hour)
	first := open()
	for index, record := range []store.APIKeyRecord{
		{ID: "22222222", Key: "fake-stored-key-000001", Label: "alpha", CreatedAt: createdAt},
		{ID: "11111111", Key: "fake-stored-key-000002", Label: "bravo", CreatedAt: createdAt.Add(time.Minute)},
	} {
		if err := first.InsertAPIKey(ctx, record); err != nil {
			t.Fatalf("InsertAPIKey(%d): %v", index, err)
		}
	}
	if err := first.RevokeAPIKey(ctx, "22222222", revokedAt); err != nil {
		t.Fatalf("RevokeAPIKey(): %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("Close(): %v", err)
	}

	second := open()
	t.Cleanup(func() { _ = second.Close() })
	listed, err := second.ListAPIKeys(ctx)
	if err != nil || len(listed) != 2 {
		t.Fatalf("ListAPIKeys() = %+v, %v, want two rows", listed, err)
	}
	if listed[0].ID != "22222222" || !listed[0].Revoked() || !listed[0].RevokedAt.Equal(revokedAt) || listed[0].Label != "alpha" || !listed[0].CreatedAt.Equal(createdAt) {
		t.Fatalf("first row = %+v, want alpha revoked at %v", listed[0], revokedAt)
	}
	if listed[1].ID != "11111111" || listed[1].Revoked() || listed[1].Key != "fake-stored-key-000002" {
		t.Fatalf("second row = %+v, want bravo active with its key", listed[1])
	}
	if record, found, err := second.APIKey(ctx, "11111111"); err != nil || !found || record.Label != "bravo" {
		t.Fatalf("APIKey(11111111) = %+v, %v, %v, want bravo", record, found, err)
	}
	if _, found, err := second.APIKey(ctx, "99999999"); err != nil || found {
		t.Fatalf("APIKey(99999999) found = %v, err = %v, want not found", found, err)
	}
	if active, err := second.ActiveAPIKeys(ctx); err != nil || !slices.Equal(active, []string{"fake-stored-key-000002"}) {
		t.Fatalf("ActiveAPIKeys() = %v, %v, want only the second key", active, err)
	}
}

// Test flow:
//  1. Insert a key and revoke it.
//  2. Insert another key under the same ID, then a different key under it, and revoke the revoked key again at a later time.
//  3. Assert both inserts fail with `store.ErrAPIKeyIDTaken`, the row is unchanged, and the first revoked time is kept.
func anAPIKeyIDIsNeverReusedAndRevokingKeepsTheFirstTime(t *testing.T, open Opener) {
	ctx := context.Background()
	records := open()
	t.Cleanup(func() { _ = records.Close() })
	createdAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := records.InsertAPIKey(ctx, store.APIKeyRecord{ID: "22222222", Key: "fake-stored-key-000001", CreatedAt: createdAt}); err != nil {
		t.Fatalf("InsertAPIKey(): %v", err)
	}
	if err := records.RevokeAPIKey(ctx, "22222222", createdAt.Add(time.Hour)); err != nil {
		t.Fatalf("RevokeAPIKey(): %v", err)
	}

	for _, key := range []string{"fake-stored-key-000001", "fake-other-key-0000002"} {
		err := records.InsertAPIKey(ctx, store.APIKeyRecord{ID: "22222222", Key: key, CreatedAt: createdAt})
		if !errors.Is(err, store.ErrAPIKeyIDTaken) {
			t.Fatalf("InsertAPIKey(%s) over a revoked ID = %v, want ErrAPIKeyIDTaken", key, err)
		}
	}
	if err := records.RevokeAPIKey(ctx, "22222222", createdAt.Add(2*time.Hour)); err != nil {
		t.Fatalf("second RevokeAPIKey(): %v", err)
	}

	record, _, err := records.APIKey(ctx, "22222222")
	if err != nil || record.Key != "fake-stored-key-000001" || !record.RevokedAt.Equal(createdAt.Add(time.Hour)) {
		t.Fatalf("APIKey() = %+v, %v, want the original key revoked at the first time", record, err)
	}
}
