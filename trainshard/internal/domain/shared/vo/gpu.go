package vo

type GPUInventory struct {
	Profile string
	Count   int
}

func (g GPUInventory) IsZero() bool { return g.Profile == "" }

func (g GPUInventory) String() string {
	if g.Profile == "" {
		return "no gpus"
	}
	return g.Profile
}
