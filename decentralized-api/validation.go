package main

import (
	"time"
)

func StartValidationScheduledTask(transactionRecorder InferenceCosmosClient) {
	// Sleep but every X seconds wake up and do the task
	for {
		time.Sleep(5 * time.Second)
		// TODO: query transaction
	}
}
