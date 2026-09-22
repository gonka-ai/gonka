//go:build devshard_testenv

package main

import "os"

const (
	envTestenvClockFaultFile = "DEVSHARD_TESTENV_CLOCK_FAULT_FILE"
	defaultClockFaultFile    = "/tmp/devshard-clock-fault"
)

func clockFaultActive() bool {
	_, err := os.Stat(clockFaultFile())
	return err == nil
}

func clockFaultFile() string {
	if p := os.Getenv(envTestenvClockFaultFile); p != "" {
		return p
	}
	return defaultClockFaultFile
}
