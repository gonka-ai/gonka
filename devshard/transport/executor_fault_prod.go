//go:build !devshard_testenv

package transport

import "devshard/host"

func executorDropsPayload() bool { return false }

func forgedChallengeReceipt(*host.Host, uint64) (*ChallengeReceiptResponse, bool) { return nil, false }
