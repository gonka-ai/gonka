package keymaterial

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"devshard/signing"
	"devshard/testenv/config"

	"github.com/cosmos/cosmos-sdk/codec"
	codectypes "github.com/cosmos/cosmos-sdk/codec/types"
	cryptocodec "github.com/cosmos/cosmos-sdk/crypto/codec"
	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	"golang.org/x/crypto/bcrypt"
)

const (
	keyringAppName = "inferenced"
	keyringFileDir = "keyring-file"
	// File keyring reads the passphrase for unlock and again per import; supply
	// enough newline-terminated copies so gencompose never prompts on a TTY.
	passphraseRepeats = 32
)

// MaterializeHosts writes a shared Cosmos file keyring under baseDir with one key
// per host (key name = host id). Keys come from hosts[].private_key_hex; derived
// addresses must match hosts[].address.
//
// baseDir is typically testenv/keyring/ (gitignored runtime data, not this package).
func MaterializeHosts(baseDir string, cfg *config.File) error {
	if cfg == nil {
		return fmt.Errorf("config is nil")
	}
	if len(cfg.Hosts) == 0 {
		return fmt.Errorf("no hosts configured")
	}

	password := cfg.Versiond.KeyringPassword
	if password == "" {
		password = config.DefaultKeyringPassword
	}

	if err := os.RemoveAll(baseDir); err != nil {
		return fmt.Errorf("reset keyring dir: %w", err)
	}
	fileDir := filepath.Join(baseDir, keyringFileDir)
	if err := os.MkdirAll(fileDir, 0o700); err != nil {
		return fmt.Errorf("mkdir keyring: %w", err)
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(password), 2)
	if err != nil {
		return fmt.Errorf("hash password: %w", err)
	}
	if err := writeKeyHash(baseDir, hash); err != nil {
		return err
	}

	kr, err := openFileKeyring(baseDir, password)
	if err != nil {
		return err
	}

	for _, h := range cfg.Hosts {
		if h.ID == "" {
			return fmt.Errorf("host id is required")
		}
		if h.PrivateKeyHex == "" {
			return fmt.Errorf("host %s: private_key_hex is required", h.ID)
		}
		if err := kr.ImportPrivKeyHex(h.ID, h.PrivateKeyHex, "secp256k1"); err != nil {
			return fmt.Errorf("import %s: %w", h.ID, err)
		}
		signer, err := signing.NewSignerFromKeyring(kr, h.ID)
		if err != nil {
			return fmt.Errorf("signer %s: %w", h.ID, err)
		}
		if h.Address != "" && signer.Address() != h.Address {
			return fmt.Errorf("host %s: address mismatch (got %s, want %s)", h.ID, signer.Address(), h.Address)
		}
	}

	return nil
}

func writeKeyHash(baseDir string, hash []byte) error {
	for _, path := range []string{
		filepath.Join(baseDir, "keyhash"),
		filepath.Join(baseDir, keyringFileDir, "keyhash"),
	} {
		if err := os.WriteFile(path, hash, 0o600); err != nil {
			return fmt.Errorf("write %s: %w", path, err)
		}
	}
	return nil
}

func passphraseReader(password string) *strings.Reader {
	var b strings.Builder
	line := password + "\n"
	for i := 0; i < passphraseRepeats; i++ {
		b.WriteString(line)
	}
	return strings.NewReader(b.String())
}

func openFileKeyring(dir, password string) (keyring.Keyring, error) {
	reg := codectypes.NewInterfaceRegistry()
	cryptocodec.RegisterInterfaces(reg)
	cdc := codec.NewProtoCodec(reg)

	kr, err := keyring.New(keyringAppName, keyring.BackendFile, dir, passphraseReader(password), cdc)
	if err != nil {
		return nil, fmt.Errorf("open file keyring: %w", err)
	}
	return kr, nil
}
