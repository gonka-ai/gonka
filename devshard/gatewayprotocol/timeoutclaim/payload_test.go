package timeoutclaim

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"devshard/internal/testutil"
	"devshard/signing"
	"devshard/state"
	"devshard/user"
)

const escrowID = "7"

func preparedParams() user.InferenceParams {
	return user.InferenceParams{
		Model:            "qwen",
		Prompt:           []byte(`{"messages":[{"role":"user","content":"hello"}],"max_tokens":64}`),
		InputLength:      512,
		MaxTokens:        64,
		ContextTotalHint: 4096,
		StartedAt:        1_700_000_000,
		Stream:           true,
	}
}

func newSession(t *testing.T) (*user.Session, *state.StateMachine) {
	t.Helper()
	signers := []*signing.Secp256k1Signer{testutil.MustGenerateKey(t), testutil.MustGenerateKey(t)}
	group := testutil.MakeGroup(signers)
	config := testutil.DefaultConfig(len(group))
	creator := testutil.MustGenerateKey(t)
	verifier := signing.NewSecp256k1Verifier()
	store := testutil.MustMemoryStore(t, escrowID, creator.Address(), config, group, 1_000_000)
	machine, err := state.NewStateMachine(escrowID, config, group, 1_000_000, creator.Address(), verifier, store)
	if err != nil {
		t.Fatalf("NewStateMachine = %v, want nil", err)
	}
	session, err := user.NewSession(machine, creator, escrowID, group, make([]user.HostClient, len(group)), verifier, user.WithStorage(store))
	if err != nil {
		t.Fatalf("NewSession = %v, want nil", err)
	}
	t.Cleanup(func() { _ = session.Close() })
	return session, machine
}

// Test flow:
//  1. Define a table of dispatch params varying across every payload field distinct and non-zero, the fields the payload does not carry (context hint, stream, height-sync anchor, logprobs override) set and unset, a long prompt and a prompt with tools and tool calls.
//  2. For each case, prepare one inference with those params through a real session over an in-memory state machine.
//  3. Assert `Payload` over the machine's live records and the params' prompt equals the payload the prepared inference was dispatched with, which is built from the params alone.
func TestPayloadOfANonceWithALiveRecordEqualsItsDispatchedPayload(t *testing.T) {
	logprobsOverride := true
	cases := []struct {
		name   string
		params func() user.InferenceParams
	}{
		{name: "every payload field distinct and non-zero", params: preparedParams},
		{
			name: "fields the payload does not carry all unset",
			params: func() user.InferenceParams {
				params := preparedParams()
				params.ContextTotalHint, params.Stream = 0, false
				return params
			},
		},
		{
			name: "fields the payload does not carry all set",
			params: func() user.InferenceParams {
				params := preparedParams()
				params.ForceHeightSyncAnchor, params.LogprobsOptimizationOverride = true, &logprobsOverride
				return params
			},
		},
		{
			name: "a long prompt",
			params: func() user.InferenceParams {
				params := preparedParams()
				params.Prompt = []byte(`{"messages":[{"role":"user","content":"` + strings.Repeat("token ", 1<<16) + `"}],"max_tokens":64}`)
				params.InputLength = uint64(len(params.Prompt))
				return params
			},
		},
		{
			name: "a prompt with tools and tool calls",
			params: func() user.InferenceParams {
				params := preparedParams()
				params.Prompt = []byte(`{"messages":[{"role":"user","content":"weather?"},{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"weather","arguments":"{\"city\":\"Oslo\"}"}}]},{"role":"tool","tool_call_id":"call_1","content":"sunny"}],"tools":[{"type":"function","function":{"name":"weather","parameters":{"type":"object","properties":{"city":{"type":"string"}}}}}],"max_tokens":64}`)
				return params
			},
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			session, machine := newSession(t)
			params := testCase.params()
			prepared, err := session.PrepareInference(context.Background(), params)
			if err != nil {
				t.Fatalf("PrepareInference = %v, want nil", err)
			}

			payload := Payload(machine.GetInference, prepared.Nonce(), params.Prompt, params.StartedAt)

			if want := prepared.Payload(); !reflect.DeepEqual(payload, want) {
				t.Errorf("Payload = %+v, want the dispatched payload %+v", payload, want)
			}
		})
	}
}

// Test flow:
//  1. Prepare one inference through a real session over an in-memory state machine and seal its record out of the live state, as the settlement drain does.
//  2. Define a table of nonces with no live record: the sealed one and one never prepared.
//  3. Assert `Payload` returns none for each.
func TestPayloadOfANonceWithoutALiveRecordIsNone(t *testing.T) {
	session, machine := newSession(t)
	params := preparedParams()
	prepared, err := session.PrepareInference(context.Background(), params)
	if err != nil {
		t.Fatalf("PrepareInference = %v, want nil", err)
	}
	if err := machine.SealInference(prepared.Nonce()); err != nil {
		t.Fatalf("SealInference = %v, want nil", err)
	}
	cases := []struct {
		name  string
		nonce uint64
	}{
		{name: "a sealed nonce", nonce: prepared.Nonce()},
		{name: "a nonce never prepared", nonce: prepared.Nonce() + 1},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if payload := Payload(machine.GetInference, testCase.nonce, params.Prompt, params.StartedAt); payload != nil {
				t.Errorf("Payload = %+v, want nil", payload)
			}
		})
	}
}
