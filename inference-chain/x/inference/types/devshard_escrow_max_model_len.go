package types

import (
	"strconv"
	"strings"
)

const devshardMaxModelLenFlag = "--max-model-len"

// DevshardMaxModelLenForCreate reads --max-model-len from the snapshot's vLLM args the way vLLM does: underscores equal dashes and the last occurrence wins; 0 when unreadable.
func DevshardMaxModelLenForCreate(model *Model) uint64 {
	if model == nil {
		return 0
	}
	lastValue := ""
	for i, arg := range model.ModelArgs {
		flag, value, isJoined := strings.Cut(arg, "=")
		if strings.ReplaceAll(flag, "_", "-") != devshardMaxModelLenFlag {
			continue
		}
		switch {
		case isJoined:
			lastValue = value
		case i+1 < len(model.ModelArgs):
			lastValue = model.ModelArgs[i+1]
		default:
			lastValue = ""
		}
	}
	return parseDevshardMaxModelLen(lastValue)
}

func parseDevshardMaxModelLen(value string) uint64 {
	maxModelLen, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0
	}
	return maxModelLen
}
