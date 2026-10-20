package callbackauth

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"hash"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

const (
	SecretSize = 32
	tokenSize  = 32

	domain         = "gonka-poc-cb/v1"
	kindGeneration = "gen"
	kindValidation = "val"
)

var tokenSegment = regexp.MustCompile(`^(/v2/poc-batches/[^/]+/(?:v/[^/]+/[^/]+/|g/))[^/]+`)

type Signer struct {
	secret []byte
}

func NewSigner(secret []byte) (*Signer, error) {
	if len(secret) != SecretSize {
		return nil, fmt.Errorf("callback secret must be %d bytes, got %d", SecretSize, len(secret))
	}
	return &Signer{secret: bytes.Clone(secret)}, nil
}

func LoadOrCreate(path string) (*Signer, error) {
	secret, err := os.ReadFile(path)
	if err == nil {
		return NewSigner(secret)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("read callback secret %s: %w", path, err)
	}

	secret = make([]byte, SecretSize)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("generate callback secret: %w", err)
	}
	if err := writeNewSecret(path, secret); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return LoadOrCreate(path)
		}
		return nil, err
	}
	return NewSigner(secret)
}

func writeNewSecret(path string, secret []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create callback secret dir %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create callback secret temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)

	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod callback secret: %w", err)
	}
	if _, err := tmp.Write(secret); err != nil {
		tmp.Close()
		return fmt.Errorf("write callback secret: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return fmt.Errorf("sync callback secret: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close callback secret: %w", err)
	}
	if err := os.Link(tmpPath, path); err != nil {
		return fmt.Errorf("install callback secret %s: %w", path, err)
	}
	return nil
}

func (s *Signer) ValidationToken(stage int64, modelID, pubKey string, sampled int64, minCount uint32) string {
	return s.token(kindValidation, stage, modelID, strings.ToLower(pubKey), sampled, int64(minCount))
}

func (s *Signer) VerifyValidation(token string, stage int64, modelID, pubKey string, sampled int64, minCount uint32) bool {
	if s == nil {
		return false
	}
	return hmac.Equal([]byte(token), []byte(s.ValidationToken(stage, modelID, pubKey, sampled, minCount)))
}

func (s *Signer) GenerationToken(stage int64, modelID string, perModel bool, nodeID int) string {
	return s.token(kindGeneration, stage, modelID, generationSubject(perModel, nodeID), 0)
}

func (s *Signer) VerifyGeneration(token string, stage int64, modelID string, perModel bool, nodeID int) bool {
	if s == nil {
		return false
	}
	return hmac.Equal([]byte(token), []byte(s.GenerationToken(stage, modelID, perModel, nodeID)))
}

func ValidationPath(sampled int64, minCount uint32, token string) string {
	return "/v/" + strconv.FormatInt(sampled, 10) + "/" + strconv.FormatUint(uint64(minCount), 10) + "/" + token
}

func GenerationPath(token string) string {
	return "/g/" + token
}

func EncodeModelID(modelID string) string {
	escaped := url.PathEscape(modelID)
	if modelID == "." || modelID == ".." {
		escaped = strings.ReplaceAll(modelID, ".", "%2E")
	}
	return url.PathEscape(escaped)
}

func RedactPath(path string) string {
	return tokenSegment.ReplaceAllString(path, "${1}***")
}

func generationSubject(perModel bool, nodeID int) string {
	if perModel {
		return "m:" + strconv.Itoa(nodeID)
	}
	return "n:" + strconv.Itoa(nodeID)
}

func (s *Signer) token(kind string, stage int64, modelID, subject string, counts ...int64) string {
	mac := hmac.New(sha256.New, s.secret)
	fields := []string{domain, kind, strconv.FormatInt(stage, 10), modelID, subject}
	for _, count := range counts {
		fields = append(fields, strconv.FormatInt(count, 10))
	}
	for _, field := range fields {
		writeField(mac, field)
	}
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))[:tokenSize]
}

func writeField(mac hash.Hash, field string) {
	mac.Write([]byte(strconv.Itoa(len(field))))
	mac.Write([]byte{':'})
	mac.Write([]byte(field))
}
