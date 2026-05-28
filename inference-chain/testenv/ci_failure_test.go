package testenv

import "testing"

func TestIntentionalCiFailure(t *testing.T) {
	t.Fatal("intentional failure to exercise PR checks")
}
