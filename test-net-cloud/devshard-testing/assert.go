package main

import "fmt"

func assertResults(responses []string, settlements []settlement) error {
	for i, r := range responses {
		if r == "" {
			return fmt.Errorf("escrow index %d: empty inference response", i)
		}
	}

	for i, s := range settlements {
		if s.Nonce == 0 {
			return fmt.Errorf("escrow index %d: nonce is 0", i)
		}
		if len(s.Signatures) == 0 {
			return fmt.Errorf("escrow index %d: no signatures in settlement", i)
		}
		var totalCost uint64
		for _, h := range s.HostStats {
			totalCost += h.Cost
		}
		if totalCost == 0 {
			return fmt.Errorf("escrow index %d: total host cost is 0", i)
		}
	}

	if len(settlements) > 1 {
		slot0 := slotIDSet(settlements[0])
		for i := 1; i < len(settlements); i++ {
			if setsEqual(slot0, slotIDSet(settlements[i])) {
				return fmt.Errorf("escrows 0 and %d have identical slot ID sets %v — may be the same instance", i, slot0)
			}
		}
	}

	return nil
}

func slotIDSet(s settlement) map[uint32]struct{} {
	m := make(map[uint32]struct{}, len(s.HostStats))
	for _, h := range s.HostStats {
		m[h.SlotID] = struct{}{}
	}
	return m
}

func setsEqual(a, b map[uint32]struct{}) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if _, ok := b[k]; !ok {
			return false
		}
	}
	return true
}
