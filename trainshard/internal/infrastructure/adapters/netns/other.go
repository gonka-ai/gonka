//go:build !linux

package netns

import (
	"errors"

	"golang.zx2c4.com/wireguard/wgctrl"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// Namespaces, wireguard links and nftables rules exist on linux only, which is where the daemon
// runs. The rest of the module stays buildable and testable elsewhere
var errPlatform = errors.New("mesh networking needs linux")

func present(int, string) (bool, error) { return false, errPlatform }

func remove(int, string) error { return errPlatform }

func raise(int, string, string) error { return errPlatform }

func build(string, wgtypes.Config, int) error { return errPlatform }

func inNetns(int, func(*wgctrl.Client) error) error { return errPlatform }

func fence(int, string, []string, []allowance) error { return errPlatform }
