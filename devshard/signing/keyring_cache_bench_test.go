package signing

import (
	"testing"

	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	sdk "github.com/cosmos/cosmos-sdk/types"
	"github.com/cosmos/cosmos-sdk/types/tx/signing"
)

// BenchmarkPayloadSign compares one payload-auth signature on a real file keyring.
//
// keyring is the previous path: SignByAddress decrypts the file three times
// (address record, info record, then Sign reads the info record again).
// cached keeps the private key in memory and stats the key file.
func BenchmarkPayloadSign(b *testing.B) {
	dir := b.TempDir()
	kr := openTestFileKeyring(b, dir, "test-pass")
	key, err := GenerateKey()
	if err != nil {
		b.Fatal(err)
	}
	if err := kr.ImportPrivKeyHex("warm", key.PrivateKeyHex(), "secp256k1"); err != nil {
		b.Fatal(err)
	}
	record, err := kr.Key("warm")
	if err != nil {
		b.Fatal(err)
	}
	addr, err := record.GetAddress()
	if err != nil {
		b.Fatal(err)
	}
	cached, err := NewCachedCosmosSigner(kr, "warm", FileKeyringInfoPath(dir, "warm"))
	if err != nil {
		b.Fatal(err)
	}
	msg := []byte("payload-auth")

	b.Run("keyring", func(b *testing.B) {
		signKeyring(b, kr, addr, msg)
	})
	b.Run("cached", func(b *testing.B) {
		signCached(b, cached, msg)
	})
	b.Run("keyring-parallel", func(b *testing.B) {
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				if _, _, err := kr.SignByAddress(addr, msg, signing.SignMode_SIGN_MODE_DIRECT); err != nil {
					b.Fatal(err)
				}
			}
		})
	})
	b.Run("cached-parallel", func(b *testing.B) {
		b.ReportAllocs()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				if _, err := cached.SignBytes(msg); err != nil {
					b.Fatal(err)
				}
			}
		})
	})
}

func signKeyring(b *testing.B, kr keyring.Keyring, addr sdk.AccAddress, msg []byte) {
	b.Helper()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, _, err := kr.SignByAddress(addr, msg, signing.SignMode_SIGN_MODE_DIRECT); err != nil {
			b.Fatal(err)
		}
	}
}

func signCached(b *testing.B, cached *CachedCosmosSigner, msg []byte) {
	b.Helper()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := cached.SignBytes(msg); err != nil {
			b.Fatal(err)
		}
	}
}
