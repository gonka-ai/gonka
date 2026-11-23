package chain

import "errors"

// ErrDevshardBusy means an escrow cannot be settled yet; registry raises it and escrow waits on it.
var ErrDevshardBusy = errors.New("devshard busy")
