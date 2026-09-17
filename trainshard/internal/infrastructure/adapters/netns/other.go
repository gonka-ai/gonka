//go:build !linux

package netns

import (
	"context"
	"errors"

	"golang.zx2c4.com/wireguard/wgctrl"
	"golang.zx2c4.com/wireguard/wgctrl/wgtypes"
)

// Namespaces, wireguard links and nftables rules exist on linux only, which is where the daemon
// runs. The rest of the module stays buildable and testable elsewhere
var errPlatform = errors.New("mesh networking needs linux")

func present(int, string) (bool, error) { return false, errPlatform }

func holds(int, string, string, wgtypes.Config) (bool, error) { return false, errPlatform }

func remove(int, string) error { return errPlatform }

func raise(int, string, string) error { return errPlatform }

func build(string, wgtypes.Config, int) error { return errPlatform }

func discard(string) error { return errPlatform }

func withWG(int, func(*wgctrl.Client) error) error { return errPlatform }

func listen(context.Context, int) error { return errPlatform }

func fenced(int) (bool, error) { return false, errPlatform }

func fence(int, string, []string, []allowance) error { return errPlatform }
