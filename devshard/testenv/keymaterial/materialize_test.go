package keymaterial

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"devshard/signing"
	"devshard/testenv/config"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	cryptokring "github.com/cosmos/cosmos-sdk/crypto/keyring"
	"github.com/stretchr/testify/require"
)

func TestMaterializeHosts_SharedKeyring(t *testing.T) {
	dir := t.TempDir()
	baseDir := filepath.Join(dir, "keyring")

	signer, err := signing.GenerateKey()
	require.NoError(t, err)

	cfg := &config.File{}
	cfg.ApplyDefaults()
	cfg.Hosts = []config.HostCfg{{
		ID:            "versiond-0",
		PrivateKeyHex: signer.PrivateKeyHex(),
		Address:       signer.Address(),
	}}

	require.NoError(t, MaterializeHosts(baseDir, cfg))

	_, err = os.Stat(filepath.Join(baseDir, "keyhash"))
	require.NoError(t, err)

	hostDir := filepath.Join(baseDir, "versiond-0")
	_, err = os.Stat(hostDir)
	require.Error(t, err, "legacy per-host keyring dir should not exist")

	kr, err := openFileKeyring(baseDir, cfg.Versiond.KeyringPassword)
	require.NoError(t, err)

	got, err := signing.NewSignerFromKeyring(kr, "versiond-0")
	require.NoError(t, err)
	require.Equal(t, signer.Address(), got.Address())
}

func TestMaterializeHosts_MultipleHosts_NoTTY(t *testing.T) {
	dir := t.TempDir()
	cfg := &config.File{}
	cfg.ApplyDefaults()
	cfg.Hosts = make([]config.HostCfg, 3)
	for i := range cfg.Hosts {
		signer, err := signing.GenerateKey()
		require.NoError(t, err)
		cfg.Hosts[i] = config.HostCfg{
			ID:            fmt.Sprintf("versiond-%d", i),
			PrivateKeyHex: signer.PrivateKeyHex(),
			Address:       signer.Address(),
		}
	}
	require.NoError(t, MaterializeHosts(dir, cfg))
}

func TestUnlockStyleMatchesDevshardd(t *testing.T) {
	dir := t.TempDir()
	signer, err := signing.GenerateKey()
	require.NoError(t, err)

	cfg := &config.File{}
	cfg.ApplyDefaults()
	cfg.Hosts = []config.HostCfg{{
		ID:            "versiond-0",
		PrivateKeyHex: signer.PrivateKeyHex(),
		Address:       signer.Address(),
	}}
	require.NoError(t, MaterializeHosts(dir, cfg))

	reg := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(reg)
	cdc := codec.NewProtoCodec(reg)

	kr, err := cryptokring.New("inferenced", cryptokring.BackendFile, dir,
		strings.NewReader(cfg.Versiond.KeyringPassword), cdc)
	require.NoError(t, err)
	_, err = kr.Key("versiond-0")
	require.NoError(t, err, "devshardd-style unlock should work")
}
