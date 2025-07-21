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

	newSharePerWorker := actualCost / int64(totalWorkers)
	remainder := actualCost % int64(totalWorkers)

	if len(existingWorkers) == 0 {
		for i, worker := range newWorkers {
			share := newSharePerWorker
			if i == 0 {
				share += remainder
			}
			actions = append(actions, Adjustment{
				WorkAdjustment: share,
				ParticipantId:  worker,
			})
		}
		return actions
	}

	oldSharePerWorker := actualCost / int64(len(existingWorkers))
	oldRemainder := actualCost % int64(len(existingWorkers))

	var totalAdjustments int64

	for i, worker := range existingWorkers {
		var currentShare int64
		var targetShare int64

		currentShare = oldSharePerWorker
		if i == 0 {
			currentShare += oldRemainder
		}

		targetShare = newSharePerWorker
		if i == 0 && remainder > 0 {
			targetShare += remainder
		}

		deductAmount := currentShare - targetShare
		if deductAmount != 0 {
			actions = append(actions, Adjustment{
				WorkAdjustment: -deductAmount,
				ParticipantId:  worker,
			})
			totalAdjustments += -deductAmount
		}
	}

	for _, worker := range newWorkers {
		actions = append(actions, Adjustment{
			WorkAdjustment: newSharePerWorker,
			ParticipantId:  worker,
		})
		totalAdjustments += newSharePerWorker
	}

	if totalAdjustments != 0 {
		for i := range actions {
			if actions[i].ParticipantId == existingWorkers[0] {
				actions[i].WorkAdjustment -= totalAdjustments
				break
			}
		}
	}

	return actions
}
