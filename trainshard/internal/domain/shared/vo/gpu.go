package vo

import "fmt"

type GPUInventory struct {
	Model string
	Count int
}

func (g GPUInventory) String() string { return fmt.Sprintf("%d x %s", g.Count, g.Model) }
