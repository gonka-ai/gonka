package signing

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	"github.com/cosmos/cosmos-sdk/types/tx/signing"
	"github.com/stretchr/testify/require"
)

func TestCachedCosmosSigner_MatchesKeyringSign(t *testing.T) {
	kr := newTestKeyring(t, "test")
	signer, err := NewCachedCosmosSigner(kr, "test", "")
	require.NoError(t, err)

	msg := []byte("payload-auth")
	got, err := signer.SignBytes(msg)
	require.NoError(t, err)

	want, _, err := kr.Sign("test", msg, signing.SignMode_SIGN_MODE_DIRECT)
	require.NoError(t, err)
	require.Equal(t, base64.StdEncoding.EncodeToString(want), got)

	again, err := signer.SignBytes(msg)
	require.NoError(t, err)
	require.Equal(t, got, again)
}

func TestCachedCosmosSigner_ReloadsWhenKeyFileChanges(t *testing.T) {
	dir := t.TempDir()
	kr := openTestFileKeyring(t, dir, "test-pass")
	counter := &exportCounter{Keyring: kr}

	firstKey, err := GenerateKey()
	require.NoError(t, err)
	secondKey, err := GenerateKey()
	require.NoError(t, err)
	require.NoError(t, kr.ImportPrivKeyHex("warm", firstKey.PrivateKeyHex(), "secp256k1"))

	infoPath := FileKeyringInfoPath(dir, "warm")
	_, err = os.Stat(infoPath)
	require.NoError(t, err)

	signer, err := NewCachedCosmosSigner(counter, "warm", infoPath)
	require.NoError(t, err)
	require.Equal(t, int32(1), counter.n.Load())

	msg := []byte("payload-auth")
	first, err := signer.SignBytes(msg)
	require.NoError(t, err)
	second, err := signer.SignBytes(msg)
	require.NoError(t, err)
	require.Equal(t, first, second)
	require.Equal(t, int32(1), counter.n.Load(), "unchanged key file must not be decrypted again")

	require.NoError(t, kr.Delete("warm"))
	require.NoError(t, kr.ImportPrivKeyHex("warm", secondKey.PrivateKeyHex(), "secp256k1"))

	third, err := signer.SignBytes(msg)
	require.NoError(t, err)
	require.NotEqual(t, first, third)
	require.Equal(t, int32(2), counter.n.Load())

	want, _, err := kr.Sign("warm", msg, signing.SignMode_SIGN_MODE_DIRECT)
	require.NoError(t, err)
	require.Equal(t, base64.StdEncoding.EncodeToString(want), third)

	_, err = signer.SignBytes(msg)
	require.NoError(t, err)
	require.Equal(t, int32(2), counter.n.Load())
}

func TestCachedCosmosSigner_KeepsPreviousKeyWhenReloadFails(t *testing.T) {
	dir := t.TempDir()
	kr := openTestFileKeyring(t, dir, "test-pass")
	counter := &exportCounter{Keyring: kr}

	key, err := GenerateKey()
	require.NoError(t, err)
	require.NoError(t, kr.ImportPrivKeyHex("warm", key.PrivateKeyHex(), "secp256k1"))

	infoPath := FileKeyringInfoPath(dir, "warm")
	signer, err := NewCachedCosmosSigner(counter, "warm", infoPath)
	require.NoError(t, err)

	record, err := kr.Key("warm")
	require.NoError(t, err)
	pub, err := record.GetPubKey()
	require.NoError(t, err)

	msg := []byte("payload-auth")
	signed, err := signer.SignBytes(msg)
	require.NoError(t, err)
	raw, err := base64.StdEncoding.DecodeString(signed)
	require.NoError(t, err)
	require.True(t, pub.VerifySignature(msg, raw))

	require.NoError(t, os.WriteFile(infoPath, []byte("not-a-key"), 0o600))

	kept, err := signer.SignBytes(msg)
	require.NoError(t, err)
	raw, err = base64.StdEncoding.DecodeString(kept)
	require.NoError(t, err)
	require.True(t, pub.VerifySignature(msg, raw))
	require.Equal(t, int32(2), counter.n.Load(), "the bad file is decrypted once")

	_, err = signer.SignBytes(msg)
	require.NoError(t, err)
	require.Equal(t, int32(2), counter.n.Load(), "a rejected file stamp is not decrypted again")
}

func TestCachedCosmosSigner_KeepsPreviousKeyWhenFileDisappears(t *testing.T) {
	dir := t.TempDir()
	kr := openTestFileKeyring(t, dir, "test-pass")
	counter := &exportCounter{Keyring: kr}

	key, err := GenerateKey()
	require.NoError(t, err)
	require.NoError(t, kr.ImportPrivKeyHex("warm", key.PrivateKeyHex(), "secp256k1"))

	infoPath := FileKeyringInfoPath(dir, "warm")
	signer, err := NewCachedCosmosSigner(counter, "warm", infoPath)
	require.NoError(t, err)

	record, err := kr.Key("warm")
	require.NoError(t, err)
	pub, err := record.GetPubKey()
	require.NoError(t, err)

	require.NoError(t, os.Remove(infoPath))

	msg := []byte("payload-auth")
	signed, err := signer.SignBytes(msg)
	require.NoError(t, err)
	require.Equal(t, int32(1), counter.n.Load(), "a missing file is not a decrypt")

	raw, err := base64.StdEncoding.DecodeString(signed)
	require.NoError(t, err)
	require.True(t, pub.VerifySignature(msg, raw))
}

func TestFileKeyringInfoPath_EncodesReservedCharacters(t *testing.T) {
	got := FileKeyringInfoPath("/keys", "warm/key%1")
	require.Equal(t, filepath.Join("/keys", "keyring-file", "warm%2Fkey%251.info"), got)
}

type exportCounter struct {
	keyring.Keyring
	n atomic.Int32
}

func (e *exportCounter) ExportPrivKeyArmor(uid, passphrase string) (string, error) {
	e.n.Add(1)
	return e.Keyring.ExportPrivKeyArmor(uid, passphrase)
}

func openTestFileKeyring(t testing.TB, dir, password string) keyring.Keyring {
	t.Helper()
	registry := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(registry)
	cdc := codec.NewProtoCodec(registry)

	var passwords strings.Builder
	for i := 0; i < 16; i++ {
		passwords.WriteString(password)
		passwords.WriteByte('\n')
	}
	kr, err := keyring.New("inferenced", keyring.BackendFile, dir, strings.NewReader(passwords.String()), cdc)
	require.NoError(t, err)
	return kr
}
