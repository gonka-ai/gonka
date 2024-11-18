package types

import (
	"github.com/google/uuid"
	"strconv"
)

const PocBatchKeyPrefix = "PoCBatch/value/"

func PoCBatchKey(pocStageStartBlockHeight int64, participantIndex string, batchId string) []byte {
	var key []byte

	key = append(key, []byte(strconv.FormatInt(pocStageStartBlockHeight, 10))...)
	key = append(key, []byte("/")...)
	key = append(key, []byte(participantIndex)...)
	key = append(key, []byte("/")...)
	key = append(key, []byte(batchId)...)
	key = append(key, []byte("/")...)

	return key
}

func GenerateBatchID() string {
	return uuid.New().String()
}
