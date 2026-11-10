// Package sharefile defines the on-disk format the decentralized API (dAPI) uses
// to hand its recovered BLS slot shares to the chain node, which needs them to
// sign per-block beacon partials inside ExtendVote (Step 2 of
// devshard/docs/proposals/block-bls-beacon-vote-extensions-plan.md).
//
// The dAPI is the only process that can decrypt shares, and ExtendVote must
// never block on a network call, so the dAPI writes one file per epoch into a
// directory both containers mount (`<home>/bls-beacon/epoch-<N>.json`) and the
// node reads it into memory.
//
// This package has no keeper or app dependencies so the dAPI can import it.
// It only checks the file's own consistency (version, checksum, lengths, field
// ranges). Checking the shares against committed state (group public key and
// per-slot public keys) is the node's job, see app/beacon.
package sharefile

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"github.com/consensys/gnark-crypto/ecc/bls12-381/fr"
)

const (
	// Version is the only file format version this package reads and writes.
	Version = 1

	// DirMode and FileMode: shares are secret key material.
	DirMode  os.FileMode = 0o700
	FileMode os.FileMode = 0o600

	// GroupPublicKeyLen is a compressed G2 point.
	GroupPublicKeyLen = 96
	// ScalarLen is one fr element.
	ScalarLen = 32

	// MaxSlots bounds a file's slot range so a corrupt file cannot make the
	// reader allocate without limit. Far above any realistic i_total_slots.
	MaxSlots = 1 << 16
	// MaxFileBytes bounds what a reader loads from disk.
	MaxFileBytes = 8 << 20

	// DefaultDirName is the directory under the node home (`/root/.inference`).
	DefaultDirName = "bls-beacon"
)

var (
	// ErrChecksum is returned when the sha256 field does not match the content.
	ErrChecksum = errors.New("sharefile: checksum mismatch")
	// ErrChainID is returned when the file belongs to another chain.
	ErrChainID = errors.New("sharefile: chain_id mismatch")

	fileNameRE = regexp.MustCompile(`^epoch-([0-9]{1,20})\.json$`)
	frModulus  = fr.Modulus()
)

// File is the JSON document written per epoch. Field order is the canonical
// order used for the checksum.
type File struct {
	Version            int      `json:"version"`
	ChainID            string   `json:"chain_id"`
	EpochID            uint64   `json:"epoch_id"`
	ParticipantAddress string   `json:"participant_address"`
	SlotStartIndex     uint32   `json:"slot_start_index"`
	SlotEndIndex       uint32   `json:"slot_end_index"`
	GroupPublicKey     string   `json:"group_public_key"` // hex, 96-byte compressed G2
	Shares             []string `json:"shares"`           // hex, 32-byte big-endian fr, one per slot
	// CreatedAt is informational and NOT covered by the checksum, so re-exporting
	// identical shares is detected as a no-op.
	CreatedAt string `json:"created_at,omitempty"`
	SHA256    string `json:"sha256,omitempty"`
}

// FileName returns the file name for an epoch.
func FileName(epochID uint64) string {
	return fmt.Sprintf("epoch-%d.json", epochID)
}

// ParseFileName returns the epoch of a share file name, or false if the name is
// not a share file.
func ParseFileName(name string) (uint64, bool) {
	m := fileNameRE.FindStringSubmatch(name)
	if m == nil {
		return 0, false
	}
	id, err := strconv.ParseUint(m[1], 10, 64)
	if err != nil {
		return 0, false
	}
	return id, true
}

// New builds a file from raw values. shares are big-endian fr encodings (as
// returned by fr.Element.Bytes), one per slot in [slotStart, slotEnd].
func New(chainID string, epochID uint64, participant string, slotStart, slotEnd uint32, groupPublicKey []byte, shares [][ScalarLen]byte, now time.Time) (*File, error) {
	f := &File{
		Version:            Version,
		ChainID:            chainID,
		EpochID:            epochID,
		ParticipantAddress: participant,
		SlotStartIndex:     slotStart,
		SlotEndIndex:       slotEnd,
		GroupPublicKey:     hex.EncodeToString(groupPublicKey),
		Shares:             make([]string, len(shares)),
		CreatedAt:          now.UTC().Format(time.RFC3339),
	}
	for i := range shares {
		f.Shares[i] = hex.EncodeToString(shares[i][:])
	}
	sum, err := f.Checksum()
	if err != nil {
		return nil, err
	}
	f.SHA256 = sum
	if err := f.Validate(chainID); err != nil {
		return nil, err
	}
	return f, nil
}

// Checksum returns the hex sha256 of the canonical JSON encoding of f with
// CreatedAt and SHA256 cleared.
func (f *File) Checksum() (string, error) {
	c := *f
	c.CreatedAt = ""
	c.SHA256 = ""
	b, err := json.Marshal(&c)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

// NumSlots returns the size of the slot range (0 if the range is inverted).
func (f *File) NumSlots() int {
	if f.SlotEndIndex < f.SlotStartIndex {
		return 0
	}
	return int(f.SlotEndIndex-f.SlotStartIndex) + 1
}

// Validate checks the file's self-consistency. If chainID is non-empty the
// file must belong to that chain.
func (f *File) Validate(chainID string) error {
	if f.Version != Version {
		return fmt.Errorf("sharefile: unsupported version %d", f.Version)
	}
	if f.ChainID == "" {
		return errors.New("sharefile: empty chain_id")
	}
	if chainID != "" && f.ChainID != chainID {
		return fmt.Errorf("%w: file %q, expected %q", ErrChainID, f.ChainID, chainID)
	}
	if f.ParticipantAddress == "" {
		return errors.New("sharefile: empty participant_address")
	}
	if f.SlotEndIndex < f.SlotStartIndex {
		return fmt.Errorf("sharefile: invalid slot range [%d,%d]", f.SlotStartIndex, f.SlotEndIndex)
	}
	n := f.NumSlots()
	if n > MaxSlots {
		return fmt.Errorf("sharefile: slot range too large: %d", n)
	}
	if len(f.Shares) != n {
		return fmt.Errorf("sharefile: %d shares for %d slots", len(f.Shares), n)
	}
	gpk, err := hex.DecodeString(f.GroupPublicKey)
	if err != nil || len(gpk) != GroupPublicKeyLen {
		return fmt.Errorf("sharefile: group_public_key must be %d hex bytes", GroupPublicKeyLen)
	}
	for i, s := range f.Shares {
		b, err := hex.DecodeString(s)
		if err != nil || len(b) != ScalarLen {
			return fmt.Errorf("sharefile: share %d must be %d hex bytes", i, ScalarLen)
		}
		v := new(big.Int).SetBytes(b)
		if v.Sign() == 0 || v.Cmp(frModulus) >= 0 {
			return fmt.Errorf("sharefile: share %d is not a non-zero fr element", i)
		}
	}
	sum, err := f.Checksum()
	if err != nil {
		return err
	}
	if f.SHA256 != sum {
		return ErrChecksum
	}
	return nil
}

// GroupPublicKeyBytes returns the decoded group public key. Call after Validate.
func (f *File) GroupPublicKeyBytes() []byte {
	b, _ := hex.DecodeString(f.GroupPublicKey)
	return b
}

// SharesBigEndian returns the decoded shares (big-endian). Call after Validate.
func (f *File) SharesBigEndian() [][ScalarLen]byte {
	out := make([][ScalarLen]byte, len(f.Shares))
	for i, s := range f.Shares {
		b, _ := hex.DecodeString(s)
		copy(out[i][:], b)
	}
	return out
}

// Marshal encodes f as indented JSON.
func (f *File) Marshal() ([]byte, error) {
	b, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(b, '\n'), nil
}

// Unmarshal decodes a file and validates it (chainID may be empty to skip the
// chain check). Unknown fields are rejected.
func Unmarshal(data []byte, chainID string) (*File, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var f File
	if err := dec.Decode(&f); err != nil {
		return nil, fmt.Errorf("sharefile: decode: %w", err)
	}
	if dec.More() {
		return nil, errors.New("sharefile: trailing data")
	}
	if err := f.Validate(chainID); err != nil {
		return nil, err
	}
	return &f, nil
}

// ReadFile reads and validates a share file.
func ReadFile(path, chainID string) (*File, error) {
	fh, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer fh.Close()
	data, err := readLimited(fh, MaxFileBytes)
	if err != nil {
		return nil, fmt.Errorf("sharefile: read %s: %w", filepath.Base(path), err)
	}
	return Unmarshal(data, chainID)
}

func readLimited(fh *os.File, limit int64) ([]byte, error) {
	st, err := fh.Stat()
	if err != nil {
		return nil, err
	}
	if st.Size() > limit {
		return nil, fmt.Errorf("file too large: %d bytes", st.Size())
	}
	buf := make([]byte, 0, st.Size())
	b := bytes.NewBuffer(buf)
	if _, err := b.ReadFrom(fh); err != nil {
		return nil, err
	}
	if int64(b.Len()) > limit {
		return nil, fmt.Errorf("file too large: %d bytes", b.Len())
	}
	return b.Bytes(), nil
}

// EnsureDir creates dir with DirMode, or tightens an existing directory to it.
func EnsureDir(dir string) error {
	if err := os.MkdirAll(dir, DirMode); err != nil {
		return err
	}
	return os.Chmod(dir, DirMode)
}

// WriteAtomic writes f to dir/epoch-<N>.json: temp file in dir, fsync, rename,
// fsync dir. A reader never observes a partial file. If an identical file
// (same checksum) already exists, nothing is written and written is false.
func WriteAtomic(dir string, f *File) (path string, written bool, err error) {
	if err := f.Validate(""); err != nil {
		return "", false, err
	}
	if err := EnsureDir(dir); err != nil {
		return "", false, err
	}
	path = filepath.Join(dir, FileName(f.EpochID))
	if existing, err := ReadFile(path, ""); err == nil && existing.SHA256 == f.SHA256 {
		// Same content. Still enforce the mode in case someone loosened it.
		_ = os.Chmod(path, FileMode)
		return path, false, nil
	}

	data, err := f.Marshal()
	if err != nil {
		return "", false, err
	}
	tmp, err := os.CreateTemp(dir, ".epoch-*.tmp")
	if err != nil {
		return "", false, err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if err := tmp.Chmod(FileMode); err != nil {
		tmp.Close()
		cleanup()
		return "", false, err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		cleanup()
		return "", false, err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		cleanup()
		return "", false, err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return "", false, err
	}
	if err := os.Rename(tmpName, path); err != nil {
		cleanup()
		return "", false, err
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
	return path, true, nil
}

// Prune removes share files for epochs below keepFrom, and stale temp files.
// It returns the epochs removed.
func Prune(dir string, keepFrom uint64) ([]uint64, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var removed []uint64
	var firstErr error
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if matched, _ := filepath.Match(".epoch-*.tmp", name); matched {
			if info, err := e.Info(); err == nil && time.Since(info.ModTime()) > time.Minute {
				_ = os.Remove(filepath.Join(dir, name))
			}
			continue
		}
		id, ok := ParseFileName(name)
		if !ok || id >= keepFrom {
			continue
		}
		if err := os.Remove(filepath.Join(dir, name)); err != nil && firstErr == nil {
			firstErr = err
			continue
		}
		removed = append(removed, id)
	}
	return removed, firstErr
}
