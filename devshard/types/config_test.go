package types

import "testing"

func TestDefaultSealGraceNonces(t *testing.T) {
	t.Run("floor", func(t *testing.T) {
		if got := DefaultSealGraceNonces(0); got != 20 {
			t.Fatalf("DefaultSealGraceNonces(0) = %d, want 20", got)
		}
		if got := DefaultSealGraceNonces(1); got != 20 {
			t.Fatalf("DefaultSealGraceNonces(1) = %d, want 20", got)
		}
	})

	t.Run("scaled", func(t *testing.T) {
		if got := DefaultSealGraceNonces(3); got != 30 {
			t.Fatalf("DefaultSealGraceNonces(3) = %d, want 30", got)
		}
	})
}

func TestNormalizeSessionConfig_FillsSealGraceNoncesOnlyWhenUnset(t *testing.T) {
	cfg := NormalizeSessionConfig(SessionConfig{
		RefusalTimeout:   7,
		ExecutionTimeout: 9,
		TokenPrice:       11,
		ValidationRate:   1234,
	}, 4)

	if cfg.SealGraceNonces != 40 {
		t.Fatalf("NormalizeSessionConfig filled SealGraceNonces = %d, want 40", cfg.SealGraceNonces)
	}
	if cfg.RefusalTimeout != 7 || cfg.ExecutionTimeout != 9 || cfg.TokenPrice != 11 || cfg.ValidationRate != 1234 {
		t.Fatalf("NormalizeSessionConfig changed unrelated fields: %+v", cfg)
	}

	explicit := NormalizeSessionConfig(SessionConfig{SealGraceNonces: 77}, 4)
	if explicit.SealGraceNonces != 77 {
		t.Fatalf("NormalizeSessionConfig overwrote explicit SealGraceNonces: got %d, want 77", explicit.SealGraceNonces)
	}
}
