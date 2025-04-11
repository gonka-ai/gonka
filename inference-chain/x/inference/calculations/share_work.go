package calculations

type Adjustment struct {
	ParticipantId  string
	WorkAdjustment int64
}

func ShareWork(existingWorkers []string, newWorkers []string, actualCost int64) []Adjustment {
	actions := make([]Adjustment, 0)

	totalWorkers := len(existingWorkers) + len(newWorkers)
	if totalWorkers == 0 {
		return actions
	}

	// Calculate new share per worker and remainder
	newSharePerWorker := actualCost / int64(totalWorkers)
	remainder := actualCost % int64(totalWorkers)

	// Handle empty existingWorkers case
	var oldSharePerWorker int64
	var oldRemainder int64
	if len(existingWorkers) > 0 {
		oldSharePerWorker = actualCost / int64(len(existingWorkers))
		oldRemainder = actualCost % int64(len(existingWorkers))
	}

	// Deduct excess from existing workers
	for i, worker := range existingWorkers {
		deductAmount := oldSharePerWorker - newSharePerWorker
		if i == 0 && remainder > 0 {
			// First worker keeps the remainder
			deductAmount = oldSharePerWorker + oldRemainder - (newSharePerWorker + remainder)
		}
		if deductAmount > 0 {
			actions = append(actions, Adjustment{
				WorkAdjustment: -deductAmount,
				ParticipantId:  worker,
			})
		}
	}

	// Add share to new workers
	for _, worker := range newWorkers {
		actions = append(actions, Adjustment{
			WorkAdjustment: newSharePerWorker,
			ParticipantId:  worker,
		})
	}

	return actions
}
