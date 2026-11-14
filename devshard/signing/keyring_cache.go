package signing

import (
	"encoding/base64"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/cosmos/cosmos-sdk/crypto/keyring"
	cryptotypes "github.com/cosmos/cosmos-sdk/crypto/types"
)

// FileKeyringInfoPath is the on-disk record for uid in a Cosmos file keyring.
// keyringDir is the directory passed to keyring.New (the parent of keyring-file).
// The filename encoding matches 99designs/keyring, which percent-encodes '/' and '%'.
func FileKeyringInfoPath(keyringDir, uid string) string {
	return filepath.Join(keyringDir, "keyring-file", escapeKeyringFilename(uid+".info"))
}

func escapeKeyringFilename(name string) string {
	var b strings.Builder
	for _, r := range name {
		if r == '/' || r == '%' {
			fmt.Fprintf(&b, "%%%02X", r)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// CachedCosmosSigner signs payload-auth bytes with the Cosmos secp256k1
// encoding: SHA256, then 64-byte R||S, base64. That is the encoding
// calculations.ValidateSignature checks.
//
// The private key is decrypted once and kept in memory. When infoPath is
// set, every signature stats that file and decrypts again only if its
// identity (mtime, size, or inode) changed. A stat failure or a failed
// decrypt keeps the previous key, so a torn write does not break signing
// and is not retried until the file changes again.
//
// An empty infoPath disables the check. Use that for memory and OS backends,
// which have no key file to watch.
type CachedCosmosSigner struct {
	mu          sync.Mutex
	kr          keyring.Keyring
	uid         string
	infoPath    string
	priv        cryptotypes.PrivKey
	seen        keyFileStamp
	rejected    keyFileStamp
	hasRejected bool
}

// NewCachedCosmosSigner decrypts uid once. infoPath is the file to watch;
// an empty path keeps the key for the process lifetime.
func NewCachedCosmosSigner(kr keyring.Keyring, uid, infoPath string) (*CachedCosmosSigner, error) {
	priv, err := exportCosmosPrivKey(kr, uid)
	if err != nil {
		return nil, err
	}
	s := &CachedCosmosSigner{
		kr:       kr,
		uid:      uid,
		infoPath: infoPath,
		priv:     priv,
	}
	if infoPath == "" {
		return s, nil
	}
	stamp, err := statKeyFile(infoPath)
	if err != nil {
		return nil, fmt.Errorf("stat keyring file %s: %w", infoPath, err)
	}
	s.seen = stamp
	return s, nil
}

// SignBytes implements calculations.Signer.
func (s *CachedCosmosSigner) SignBytes(data []byte) (string, error) {
	priv, err := s.privateKey()
	if err != nil {
		return "", err
	}
	sig, err := priv.Sign(data)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(sig), nil
}

func (s *CachedCosmosSigner) privateKey() (cryptotypes.PrivKey, error) {
	if s.infoPath == "" {
		return s.priv, nil
	}
	stamp, statErr := statKeyFile(s.infoPath)

	s.mu.Lock()
	defer s.mu.Unlock()

	if statErr != nil {
		if s.priv != nil {
			return s.priv, nil
		}
		return nil, statErr
	}
	if s.priv != nil && stamp == s.seen {
		return s.priv, nil
	}
	if s.priv != nil && s.hasRejected && stamp == s.rejected {
		return s.priv, nil
	}

	priv, err := exportCosmosPrivKey(s.kr, s.uid)
	if err != nil {
		s.rejected = stamp
		s.hasRejected = true
		if s.priv != nil {
			slog.Warn("keyring file changed but key reload failed; signing with the previous key",
				"key", s.uid, "path", s.infoPath, "error", err)
			return s.priv, nil
		}
		return nil, err
	}
	if !s.seen.isZero() && stamp != s.seen {
		slog.Info("reloaded signing key from keyring file", "key", s.uid, "path", s.infoPath)
	}
	s.priv = priv
	s.seen = stamp
	s.hasRejected = false
	return s.priv, nil
}

type keyFileStamp struct {
	modNano int64
	size    int64
	inode   uint64
}

func (s keyFileStamp) isZero() bool {
	return s == keyFileStamp{}
}

func statKeyFile(path string) (keyFileStamp, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return keyFileStamp{}, err
	}
	stamp := keyFileStamp{
		modNano: fi.ModTime().UnixNano(),
		size:    fi.Size(),
	}
	if sys, ok := fi.Sys().(*syscall.Stat_t); ok {
		stamp.inode = sys.Ino
	}
	return stamp, nil
}
