package main

import (
	"time"

	"devshard/user"
)

type chatBudget interface {
	ChatBudgetWait() time.Duration
}

func throttledOrOutOfChatBudget(clients []user.HostClient, keys []string, limiterBlocked func(participantKey string) bool) func(participantKey string) bool {
	budgets := make(map[string]chatBudget, len(clients))
	for slot, client := range clients {
		budget, paced := client.(chatBudget)
		if !paced || slot >= len(keys) {
			continue
		}
		budgets[keys[slot]] = budget
	}
	return func(participantKey string) bool {
		if limiterBlocked != nil && limiterBlocked(participantKey) {
			return true
		}
		budget, paced := budgets[participantKey]
		return paced && budget.ChatBudgetWait() > 0
	}
}
