package callbackauth

import (
	"bytes"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func newTestSigner(t *testing.T, fill byte) *Signer {
	t.Helper()
	signer, err := NewSigner(bytes.Repeat([]byte{fill}, SecretSize))
	require.NoError(t, err)
	return signer
}

func TestNewSignerRejectsWrongSize(t *testing.T) {
	_, err := NewSigner(make([]byte, SecretSize-1))
	require.Error(t, err)
}

func TestLoadOrCreatePersistsSecret(t *testing.T) {
	path := filepath.Join(t.TempDir(), "data", "poc-callback.key")

	first, err := LoadOrCreate(path)
	require.NoError(t, err)

	info, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	second, err := LoadOrCreate(path)
	require.NoError(t, err)

	token := first.ValidationToken(100, "org/model", "02ab", 200, 0)
	require.True(t, second.VerifyValidation(token, 100, "org/model", "02ab", 200, 0))

	entries, err := os.ReadDir(filepath.Dir(path))
	require.NoError(t, err)
	require.Len(t, entries, 1)
}

func TestLoadOrCreateRejectsCorruptSecret(t *testing.T) {
	path := filepath.Join(t.TempDir(), "poc-callback.key")
	require.NoError(t, os.WriteFile(path, []byte("short"), 0o600))

	_, err := LoadOrCreate(path)
	require.Error(t, err)

	content, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, []byte("short"), content)
}

func TestValidationTokenBindsEveryField(t *testing.T) {
	signer := newTestSigner(t, 1)
	token := signer.ValidationToken(100, "org/model", "02ab", 200, 0)

	require.True(t, signer.VerifyValidation(token, 100, "org/model", "02ab", 200, 0))
	require.True(t, signer.VerifyValidation(token, 100, "org/model", "02AB", 200, 0))
	require.False(t, signer.VerifyValidation(token, 101, "org/model", "02ab", 200, 0))
	require.False(t, signer.VerifyValidation(token, 100, "org/other", "02ab", 200, 0))
	require.False(t, signer.VerifyValidation(token, 100, "org/model", "02ac", 200, 0))
	require.False(t, signer.VerifyValidation(token, 100, "org/model", "02ab", 201, 0))
	require.False(t, newTestSigner(t, 2).VerifyValidation(token, 100, "org/model", "02ab", 200, 0))
	require.False(t, signer.VerifyValidation("", 100, "org/model", "02ab", 200, 0))
}

func TestValidationTokenBindsMinCount(t *testing.T) {
	signer := newTestSigner(t, 1)
	token := signer.ValidationToken(100, "org/model", "02ab", 200, 200)

	require.True(t, signer.VerifyValidation(token, 100, "org/model", "02ab", 200, 200))
	require.False(t, signer.VerifyValidation(token, 100, "org/model", "02ab", 200, 0))
	require.False(t, signer.VerifyValidation(token, 100, "org/model", "02ab", 200, 199))
	require.Equal(t, "/v/200/200/"+token, ValidationPath(200, 200, token))
}

func TestGenerationTokenSeparatesNodeNumbering(t *testing.T) {
	signer := newTestSigner(t, 1)
	global := signer.GenerationToken(100, "model", false, 2)
	perModel := signer.GenerationToken(100, "model", true, 2)

	require.NotEqual(t, global, perModel)
	require.True(t, signer.VerifyGeneration(global, 100, "model", false, 2))
	require.False(t, signer.VerifyGeneration(global, 100, "model", true, 2))
	require.False(t, signer.VerifyGeneration(perModel, 100, "model", false, 2))
	require.False(t, signer.VerifyGeneration(global, 101, "model", false, 2))
	require.False(t, signer.VerifyGeneration(global, 100, "model", false, 3))
}

func TestTokensSeparateKinds(t *testing.T) {
	signer := newTestSigner(t, 1)
	require.NotEqual(t, signer.GenerationToken(100, "model", false, 0), signer.ValidationToken(100, "model", "n:0", 0, 0))
}

func TestFieldEncodingIsUnambiguous(t *testing.T) {
	signer := newTestSigner(t, 1)
	require.NotEqual(t,
		signer.ValidationToken(100, "a|b", "c", 1, 0),
		signer.ValidationToken(100, "a", "b|c", 1, 0),
	)
}

func TestNilSignerRejects(t *testing.T) {
	var signer *Signer
	require.False(t, signer.VerifyValidation("x", 1, "m", "p", 1, 0))
	require.False(t, signer.VerifyGeneration("x", 1, "m", false, 1))
}

func TestRedactPath(t *testing.T) {
	require.Equal(t,
		"/v2/poc-batches/org%252Fmodel/v/200/200/***/validated",
		RedactPath("/v2/poc-batches/org%252Fmodel/v/200/200/secret-token/validated"),
	)
	require.Equal(t,
		"/v2/poc-batches/model/g/***/m/generated",
		RedactPath("/v2/poc-batches/model/g/secret-token/m/generated"),
	)
	require.Equal(t, "/versions", RedactPath("/versions"))
}

func TestRedactPathRouteLikeModelIDs(t *testing.T) {
	for _, modelID := range []string{"g", "v", "model", "org/g/v/model", "with space", "模型"} {
		for _, suffix := range []string{
			"/g/sensitive-token/generated",
			"/g/sensitive-token/m/generated",
			"/v/200/200/sensitive-token/validated",
		} {
			t.Run(modelID+suffix, func(t *testing.T) {
				callbackURL := "/v2/poc-batches/" + url.PathEscape(url.PathEscape(modelID)) + suffix
				parsed, err := url.Parse(callbackURL)
				require.NoError(t, err)
				expected := strings.Replace(parsed.Path, "sensitive-token", "***", 1)
				require.Equal(t, expected, RedactPath(parsed.Path))
			})
		}
	}
}
