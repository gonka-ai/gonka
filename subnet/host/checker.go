package host

import "subnet/types"

// CompositeChecker chains multiple AcceptanceCheckers. The first non-nil
// error short-circuits and withholds the signature.
type CompositeChecker struct {
	checkers []AcceptanceChecker
}

func NewCompositeChecker(checkers ...AcceptanceChecker) *CompositeChecker {
	return &CompositeChecker{checkers: checkers}
}

func (c *CompositeChecker) Check(st types.EscrowState, applied []*types.SubnetTx) error {
	for _, ch := range c.checkers {
		if err := ch.Check(st, applied); err != nil {
			return err
		}
	}
	return nil
}
