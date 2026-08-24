package cosmos_test

import (
	"encoding/hex"
	"strings"
	"testing"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/hd"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	"github.com/cosmos/cosmos-sdk/types/bech32"

	"trainshard/internal/domain/shared/vo"
	"trainshard/internal/infrastructure/adapters/signing/cosmos"
)

const (
	alice = "1b3a2f4e5c6d7a8b9c0d1e2f3a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d"
	bob   = "6d5c4b3a29187f6e5d4c3b2a1f0e9d8c7b6a5f4e3d2c1b0a9f8e7d6c5b4a3f21"
)

func TestASignatureNamesTheAccountThatMadeIt(t *testing.T) {
	key, err := cosmos.FromHex(alice)
	if err != nil {
		t.Fatalf("got %v, want a key", err)
	}
	if !strings.HasPrefix(string(key.Address()), "gonka1") {
		t.Fatalf("got %q, want the address the chain knows this key by", key.Address())
	}

	payload := []byte("POST /shards/1/deploy")
	signed, err := cosmos.Recover(payload, key.Sign(payload))
	if err != nil || signed != key.Address() {
		t.Fatalf("got %q %v, want %q", signed, err, key.Address())
	}
}

func TestASignatureIsWorthNothingOnAnotherMessage(t *testing.T) {
	key, err := cosmos.FromHex(alice)
	if err != nil {
		t.Fatalf("got %v, want a key", err)
	}
	other, err := cosmos.FromHex(bob)
	if err != nil {
		t.Fatalf("got %v, want a key", err)
	}

	signature := key.Sign([]byte("POST /shards/1/deploy"))
	recovered, err := cosmos.Recover([]byte("POST /shards/1/abort"), signature)
	if err == nil && recovered == key.Address() {
		t.Fatal("a signature over one request must not stand for another")
	}
	if other.Address() == key.Address() {
		t.Fatal("two keys must not share an address")
	}
}

// The daemon takes the participant's key from the keyring the machine already has, on the file
// backend a join deployment uses. This is the one path between an operator's config and a daemon
// that starts at all, and the backend asks for the passphrase on its own terms
func TestTheParticipantsKeyIsTakenFromTheKeyringOnDisk(t *testing.T) {
	for _, backend := range []string{"test", "file"} {
		t.Run(backend, func(t *testing.T) {
			// arrange
			dir, password := t.TempDir(), "keyring-password"
			want := writeKey(t, dir, backend, password, "host")

			// act
			key, err := cosmos.FromKeyring(dir, backend, password, "host")

			// assert
			if err != nil {
				t.Fatalf("got %v, want the key the machine already holds", err)
			}
			if key.Address() != want {
				t.Fatalf("got %q, want %q: the daemon would refuse to speak for its participant", key.Address(), want)
			}
			payload := []byte("POST /shards/1/deploy")
			signed, err := cosmos.Recover(payload, key.Sign(payload))
			if err != nil || signed != want {
				t.Fatalf("got %q %v, want a key that signs as %q", signed, err, want)
			}
		})
	}
}

func TestAKeyringWithoutThatKeyIsNotAKey(t *testing.T) {
	dir := t.TempDir()
	writeKey(t, dir, "test", "keyring-password", "host")

	if _, err := cosmos.FromKeyring(dir, "test", "keyring-password", "someone-else"); err == nil {
		t.Fatal("a name the keyring does not hold must not produce a key")
	}
}

func writeKey(t *testing.T, dir, backend, password, name string) vo.Address {
	t.Helper()

	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)
	ring, err := keyring.New("inferenced", backend, dir, strings.NewReader(prompts(password)), codec.NewProtoCodec(registry))
	if err != nil {
		t.Fatalf("keyring: %v", err)
	}
	record, _, err := ring.NewMnemonic(name, keyring.English, "m/44'/118'/0'/0/0", keyring.DefaultBIP39Passphrase, hd.Secp256k1)
	if err != nil {
		t.Fatalf("new key: %v", err)
	}
	account, err := record.GetAddress()
	if err != nil {
		t.Fatalf("address: %v", err)
	}
	encoded, err := bech32.ConvertAndEncode("gonka", account.Bytes())
	if err != nil {
		t.Fatalf("bech32: %v", err)
	}
	return vo.Address(encoded)
}

// the file backend prompts once per read and takes the answer from the reader it was built with,
// and how many times it asks is its business, not ours
func prompts(password string) string {
	return strings.Repeat(password+"\n", 16)
}

func TestNothingSignsWithSomethingThatIsNotAKey(t *testing.T) {
	for name, raw := range map[string]string{
		"empty":      "",
		"too short":  hex.EncodeToString([]byte("short")),
		"not hex":    "zzzz",
		"32 zeroes":  strings.Repeat("0", 64),
		"64 bytes":   strings.Repeat("ab", 64),
		"odd length": "abc",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := cosmos.FromHex(raw); err == nil {
				t.Fatalf("%q was taken as a key", raw)
			}
		})
	}
}
