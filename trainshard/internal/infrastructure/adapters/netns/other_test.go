//go:build !linux

package netns

import (
	"errors"
	"testing"

	"golang.zx2c4.com/wireguard/wgctrl"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// Off linux every stub must refuse, so a namespace is never reported as set up when nothing happened
func TestUnsupportedPlatformRefuses(t *testing.T) {
	// arrange
	calls := map[string]error{}

	// act
	_, calls["present"] = present(1, "ts0")
	calls["remove"] = remove(1, "ts0")
	calls["raise"] = raise(1, "ts0", "10.42.0.1")
	calls["build"] = build("ts0", wgtypes.Config{}, 1)
	calls["inNetns"] = inNetns(1, func(*wgctrl.Client) error { return nil })
	calls["fence"] = fence(1, "ts0", nil, nil)

	// assert
	for name, err := range calls {
		if !errors.Is(err, errPlatform) {
			t.Errorf("%s = %v, want the platform refusal", name, err)
		}
	}
}
