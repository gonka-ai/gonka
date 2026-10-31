package store

import (
	"testing"

	"devshard/cmd/gateway/internal/leakcheck"
)

func TestMain(m *testing.M) {
	leakcheck.VerifyTestMain(m)
}
