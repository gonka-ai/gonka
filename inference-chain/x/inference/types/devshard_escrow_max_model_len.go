package types

import (
	"strconv"
	"strings"
)

const devshardMaxModelLenFlag = "--max-model-len"

func DevshardMaxModelLenForCreate(model *Model) uint64 {
	if model == nil {
		return 0
	}
	for i, arg := range model.ModelArgs {
		if arg == devshardMaxModelLenFlag && i+1 < len(model.ModelArgs) {
			return parseDevshardMaxModelLen(model.ModelArgs[i+1])
		}
		if value, found := strings.CutPrefix(arg, devshardMaxModelLenFlag+"="); found {
			return parseDevshardMaxModelLen(value)
		}
	}
	return 0
}

func parseDevshardMaxModelLen(value string) uint64 {
	maxModelLen, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0
	}
	return maxModelLen
}
