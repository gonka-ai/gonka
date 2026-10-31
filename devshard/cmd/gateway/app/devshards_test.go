package app

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"devshard/bridge"
	"devshard/cmd/gateway/api"
	"devshard/cmd/gateway/chain"
	"devshard/cmd/gateway/config"
	"devshard/cmd/gateway/env"
	"devshard/cmd/gateway/store"
	"devshard/transport"
)

// Test flow:
//  1. Call escrowRoutePrefix with a pinned record naming its own route prefix.
//  2. Assert the pinned prefix wins over the gateway's own running prefix.
//  3. Call escrowRoutePrefix with an unpinned record.
//  4. Assert it falls back to the gateway's own running prefix.
func TestEscrowRoutePrefixPrefersThePinOverTheRunningGateway(t *testing.T) {
	pinned := store.DevshardRecord{EscrowID: "58128", RoutePrefix: "/devshard/v3"}
	if got := escrowRoutePrefix(pinned, "/devshard/v4"); got != "/devshard/v3" {
		t.Fatalf("escrowRoutePrefix(pinned) = %q, want %q", got, "/devshard/v3")
	}

	unpinned := store.DevshardRecord{EscrowID: "58128"}
	if got := escrowRoutePrefix(unpinned, "/devshard/v4"); got != "/devshard/v4" {
		t.Fatalf("escrowRoutePrefix(unpinned) = %q, want the gateway's own %q", got, "/devshard/v4")
	}
}

type recordingRegistry struct {
	existing []store.DevshardRecord
	upserted []store.DevshardRecord
}

func (r *recordingRegistry) ListDevshards(context.Context) ([]store.DevshardRecord, error) {
	return r.existing, nil
}

func (r *recordingRegistry) UpsertDevshard(_ context.Context, record store.DevshardRecord) error {
	r.upserted = append(r.upserted, record)
	return nil
}

// Test flow:
//  1. Call seedDevshards with a seed JSON that names no private_key_env.
//  2. Assert it returns an error and stores nothing in the registry.
//  3. Call seedDevshards again with the same seed plus private_key_env set.
//  4. Assert it succeeds and stores exactly one record.
func TestSeedDevshardsRejectsASeedThatNamesNoKeyVariable(t *testing.T) {
	registry := &recordingRegistry{}
	if err := seedDevshards(context.Background(), registry, `[{"id":"58128","model":"Qwen/Test"}]`); err == nil {
		t.Fatal("seedDevshards accepted a seed with no private_key_env")
	}
	if len(registry.upserted) != 0 {
		t.Fatalf("seedDevshards stored %d records, want none", len(registry.upserted))
	}

	complete := `[{"id":"58128","model":"Qwen/Test","private_key_env":"DEVSHARD_PRIVATE_KEY"}]`
	if err := seedDevshards(context.Background(), registry, complete); err != nil {
		t.Fatalf("seedDevshards(complete) = %v, want nil", err)
	}
	if len(registry.upserted) != 1 {
		t.Fatalf("seedDevshards stored %d records, want 1", len(registry.upserted))
	}
}

// Test flow:
//  1. Call seedDevshards with a seed that names a per-escrow route prefix.
//  2. Assert the stored record carries that prefix, so the escrow's hosts are dialled under it.
func TestSeedDevshardsKeepsTheSeedsRoutePrefix(t *testing.T) {
	registry := &recordingRegistry{}
	seed := `[{"id":"58128","model":"Qwen/Test","private_key_env":"DEVSHARD_PRIVATE_KEY","route_prefix":"/v4"}]`

	if err := seedDevshards(context.Background(), registry, seed); err != nil {
		t.Fatalf("seedDevshards() = %v, want nil", err)
	}

	if len(registry.upserted) != 1 || registry.upserted[0].RoutePrefix != "/v4" {
		t.Fatalf("stored %+v, want one record with route prefix /v4", registry.upserted)
	}
}

// Test flow:
//  1. Call seedDevshards with each id that is not a canonical decimal chain escrow id, the path traversal ones included.
//  2. Assert each call returns ErrInvalidEscrowID and stores nothing.
func TestSeedDevshardsRejectsAnIDThatIsNotAChainEscrowID(t *testing.T) {
	for _, escrowID := range []string{"1/../../outside", "../outside", "escrow-1", "007", "+7", " 7", "-1", "18446744073709551616"} {
		registry := &recordingRegistry{}
		seed := fmt.Sprintf(`[{"id":%q,"model":"Qwen/Test","private_key_env":"DEVSHARD_PRIVATE_KEY"}]`, escrowID)

		err := seedDevshards(context.Background(), registry, seed)

		if !errors.Is(err, api.ErrInvalidEscrowID) {
			t.Errorf("seedDevshards(%q) = %v, want ErrInvalidEscrowID", escrowID, err)
		}
		if len(registry.upserted) != 0 {
			t.Errorf("seedDevshards(%q) stored %d records, want none", escrowID, len(registry.upserted))
		}
	}
}

type deactivationRecorder struct{ calls []string }

func (recorder *deactivationRecorder) MarkDevshardGoneFromChain(_ context.Context, escrowID string) error {
	recorder.calls = append(recorder.calls, "gone "+escrowID)
	return nil
}

func (recorder *deactivationRecorder) SetDevshardActive(_ context.Context, escrowID string, active bool) error {
	recorder.calls = append(recorder.calls, fmt.Sprintf("active %s %t", escrowID, active))
	return nil
}

func (recorder *deactivationRecorder) DeleteDevshard(_ context.Context, escrowID string) error {
	recorder.calls = append(recorder.calls, "delete "+escrowID)
	return nil
}

// Test flow:
//  1. Deactivate one row boot found absent on chain and one whose key the environment lacks.
//  2. Assert the first is marked gone from chain and the second only deactivated.
func TestABootDeactivationMarksAnEscrowTheChainLacksGone(t *testing.T) {
	recorder := &deactivationRecorder{}
	deactivate := unservableDeactivation(context.Background(), recorder)

	_ = deactivate("absent", fmt.Errorf("opening session: %w", bridge.ErrEscrowNotFound))
	_ = deactivate("keyless", fmt.Errorf("opening session: %w", env.ErrPrivateKeyMissing))

	if want := []string{"gone absent", "active keyless false"}; !slices.Equal(recorder.calls, want) {
		t.Fatalf("deactivation calls = %v, want %v", recorder.calls, want)
	}
}

type epochLookup struct {
	info  chain.EscrowInfo
	found bool
	err   error
	calls int
}

func (lookup *epochLookup) GetEscrow(context.Context, string) (chain.EscrowInfo, bool, error) {
	lookup.calls++
	return lookup.info, lookup.found, lookup.err
}

type rowLister []store.DevshardRecord

func (rows rowLister) ListDevshards(context.Context) ([]store.DevshardRecord, error) {
	return rows, nil
}

// Test flow:
//  1. Table-driven: a row with a stored chain epoch, an unresolved row the chain answers for, and an unresolved row the chain cannot answer for.
//  2. Resolve the creation epoch.
//  3. Assert the stored chain epoch wins without a chain call, then the chain's answer, then the row label.
func TestTheCreationEpochPrefersTheStoredChainEpoch(t *testing.T) {
	for _, testCase := range []struct {
		name      string
		row       store.DevshardRecord
		lookup    *epochLookup
		want      uint64
		wantCalls int
	}{
		{name: "stored chain epoch", row: store.DevshardRecord{EscrowID: "1", ChainEpoch: 7, RotationEpoch: 8}, lookup: &epochLookup{}, want: 7},
		{name: "the chain answers", row: store.DevshardRecord{EscrowID: "1", RotationEpoch: 8}, lookup: &epochLookup{info: chain.EscrowInfo{EpochIndex: 7}, found: true}, want: 7, wantCalls: 1},
		{name: "the label is the last resort", row: store.DevshardRecord{EscrowID: "1", RotationEpoch: 8}, lookup: &epochLookup{err: errors.New("unreachable")}, want: 8, wantCalls: 1},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			epoch, resolved := creationEpochOf(testCase.lookup, rowLister{testCase.row})(context.Background(), "1")

			if !resolved || epoch != testCase.want || testCase.lookup.calls != testCase.wantCalls {
				t.Fatalf("creationEpochOf(%s) = %d, %t with %d chain calls, want %d with %d", testCase.name, epoch, resolved, testCase.lookup.calls, testCase.want, testCase.wantCalls)
			}
		})
	}
}

// Test flow:
//  1. Build the peer client config with no courier and no adoption tracker, and with a courier and a tracker.
//  2. Assert both carry the chat compression threshold, and the second keeps the courier's own fields and the tracker.
func TestPeerRPCClientConfigCarriesTheChatCompressionThreshold(t *testing.T) {
	bare := peerRPCClientConfig(nil, nil)
	adoption := transport.NewPeerRPCAdoption(nil)
	courier := peerRPCClientConfig(&transport.ClientConfig{QueryTimeout: time.Second}, adoption)

	if bare == nil || bare.ChatCompressMinBytes != chatCompressMinBytes {
		t.Fatalf("bare config = %+v, want the chat compression threshold %d", bare, chatCompressMinBytes)
	}
	if courier.ChatCompressMinBytes != chatCompressMinBytes || courier.QueryTimeout != time.Second || courier.RPCAdoption != adoption {
		t.Fatalf("courier config = %+v, want its own fields, the tracker and the threshold %d", courier, chatCompressMinBytes)
	}
}

type settledChain struct {
	bridge.MainnetBridge
	escrow bridge.EscrowInfo
}

func (chain settledChain) GetEscrow(string) (*bridge.EscrowInfo, error) {
	escrow := chain.escrow
	return &escrow, nil
}

func (settledChain) GetHostInfo(address string) (*bridge.HostInfo, error) {
	return nil, fmt.Errorf("%s: %w", address, bridge.ErrParticipantNotFound)
}

// Test flow:
//  1. Store an active row for an escrow the chain reports settled, as a boot seed writes it again after settlement dropped the row.
//  2. Open a serving session for it.
//  3. Assert the open is refused with bridge.ErrEscrowSettled and no session is returned.
func TestAServingSessionRefusesAnEscrowTheChainSettled(t *testing.T) {
	t.Setenv("SETTLED_ESCROW_KEY", strings.Repeat("11", 32))
	rows := rowLister{{EscrowID: "1", PrivateKeyEnv: "SETTLED_ESCROW_KEY", Model: "model-a", Active: true}}
	chain := settledChain{escrow: bridge.EscrowInfo{EscrowID: "1", Slots: []string{"host-a", "host-b", "host-c"}, Amount: 5_000_000_000, TokenPrice: 1, Settled: true}}
	open := servingSessions(rows, t.TempDir(), chain, config.HeightSync{}, nil, nil, "/devshard/v2", nil, nil)

	session, err := open(context.Background(), "1")

	if session != nil {
		_ = session.Close()
	}
	if session != nil || !errors.Is(err, bridge.ErrEscrowSettled) {
		t.Fatalf("open(settled escrow) = %v, %v, want no session and bridge.ErrEscrowSettled", session, err)
	}
}

// Test flow:
//  1. Publish an active row whose session open is refused because the chain settled the escrow.
//  2. Assert it is reported unservable rather than left unopened for a retry.
//  3. Assert its row is dropped, as settlement drops the row of an escrow it settled.
func TestABootDropsTheRowOfAnEscrowTheChainSettled(t *testing.T) {
	recorder := &deactivationRecorder{}
	var unservable []string
	refuse := func(context.Context, store.DevshardRecord) error {
		return fmt.Errorf("opening session: %w", bridge.ErrEscrowSettled)
	}
	noRetire := func(string) error { return nil }

	unopened, err := publishEscrows(context.Background(), []store.DevshardRecord{{EscrowID: "1", Model: "model-a", Active: true}}, 1, refuse, noRetire,
		unservableDeactivation(context.Background(), recorder),
		func(escrowID string, _ error) { unservable = append(unservable, escrowID) },
		func(string, error) {})

	if err != nil || unopened != 0 || !slices.Equal(unservable, []string{"1"}) {
		t.Fatalf("publishEscrows = %d, %v with unservable %v, want 0, nil with [1]", unopened, err, unservable)
	}
	if want := []string{"delete 1"}; !slices.Equal(recorder.calls, want) {
		t.Fatalf("deactivation calls = %v, want %v", recorder.calls, want)
	}
}
