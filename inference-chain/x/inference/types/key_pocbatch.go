package types

import (
	"strconv"

	"github.com/google/uuid"
)

const (
	PocBatchKeyPrefix   = "PoCBatch/value/"
	PocValidationPrefix = "PoCValidation/value/"
)

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

func PoCBatchPrefixByStage(pocStageStartBlockHeight int64) []byte {
	return append([]byte(PocBatchKeyPrefix), []byte(strconv.FormatInt(pocStageStartBlockHeight, 10))...)
}

func GenerateBatchID() string {
	return uuid.New().String()
}

func PoCValidationKey(pocStageStartBlockHeight int64, participantIndex string, valParticipantIndex string) []byte {
	var key []byte

	key = append(key, []byte(strconv.FormatInt(pocStageStartBlockHeight, 10))...)
	key = append(key, []byte("/")...)
	key = append(key, []byte(participantIndex)...)
	key = append(key, []byte("/")...)
	key = append(key, []byte(valParticipantIndex)...)
	key = append(key, []byte("/")...)

	return key
}
