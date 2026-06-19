package storage

import "slices"

// mergeValidationObsBySlot sums validation observability rows by slot_id.
func mergeValidationObsBySlot(lists ...[]SlotValidationObs) []SlotValidationObs {
	if len(lists) == 0 {
		return nil
	}
	bySlot := make(map[uint32]SlotValidationObs)
	for _, list := range lists {
		for _, row := range list {
			cur := bySlot[row.SlotID]
			cur.SlotID = row.SlotID
			cur.RequiredValidations += row.RequiredValidations
			cur.CompletedValidations += row.CompletedValidations
			bySlot[row.SlotID] = cur
		}
	}
	if len(bySlot) == 0 {
		return nil
	}
	slotIDs := make([]uint32, 0, len(bySlot))
	for id := range bySlot {
		slotIDs = append(slotIDs, id)
	}
	slices.Sort(slotIDs)
	out := make([]SlotValidationObs, 0, len(slotIDs))
	for _, id := range slotIDs {
		out = append(out, bySlot[id])
	}
	return out
}
