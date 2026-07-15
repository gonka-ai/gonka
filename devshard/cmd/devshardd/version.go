package main

import "fmt"

const (
	printBinaryVersionFlag   = "--print-binary-version"
	printProtocolVersionFlag = "--print-protocol-version"
	printAdminAPIVersionFlag = "--print-admin-api-version"
)

func maybePrintVersionAndExit(args []string) bool {
	if len(args) != 1 {
		return false
	}
	switch args[0] {
	case printBinaryVersionFlag:
		fmt.Println(BinaryVersion)
		return true
	case printProtocolVersionFlag:
		fmt.Println(Version)
		return true
	case printAdminAPIVersionFlag:
		fmt.Println("1")
		return true
	default:
		return false
	}
}
