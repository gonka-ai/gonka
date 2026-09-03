package readiness

import (
	"fmt"

	"trainshard/internal/domain/shared/vo"
)

func From(name CheckName, err error) Check {
	if err != nil {
		return Failed(name, err.Error())
	}
	return Passed(name)
}

func SameGPUs(machine, claimed vo.GPUInventory, err error) Check {
	if err != nil {
		return Failed(CheckGPUsMatchChain, err.Error())
	}
	if machine != claimed {
		return Failed(CheckGPUsMatchChain, fmt.Sprintf("machine has %s, chain says %s", machine, claimed))
	}
	return Passed(CheckGPUsMatchChain)
}

func FreeDisk(free, needed int64, err error) Check {
	if err != nil {
		return Failed(CheckFreeDisk, err.Error())
	}
	if free < needed {
		return Failed(CheckFreeDisk, fmt.Sprintf("%d bytes free, %d needed", free, needed))
	}
	return Passed(CheckFreeDisk)
}

func SupportedBuild(running, supported string) Check {
	if supported != "" && supported != running {
		return Failed(CheckVersion, fmt.Sprintf("running %s, supported %s", running, supported))
	}
	return Passed(CheckVersion)
}
