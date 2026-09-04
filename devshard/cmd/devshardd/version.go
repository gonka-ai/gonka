package main

import (
	"context"
	"fmt"
	"io"

	"common/storage/mode"
	"devshard/storage"
)

const (
	printBinaryVersionFlag   = "--print-binary-version"
	printProtocolVersionFlag = "--print-protocol-version"
	printAdminAPIVersionFlag = "--print-admin-api-version"
	printStorageModeFlag     = "--print-storage-mode"
	initializePostgresFlag   = "--initialize-postgres-schema"
)

func maybeInitializePostgres(ctx context.Context, args []string, stderr io.Writer) (int, bool) {
	if len(args) != 1 || args[0] != initializePostgresFlag {
		return 0, false
	}
	if err := storage.InitializePostgresSchema(ctx); err != nil {
		fmt.Fprintln(stderr, err)
		return 1, true
	}
	return 0, true
}

func maybePrintVersion(args []string, stdout, stderr io.Writer) (int, bool) {
	if len(args) != 1 {
		return 0, false
	}
	switch args[0] {
	case printBinaryVersionFlag:
		fmt.Fprintln(stdout, BinaryVersion)
		return 0, true
	case printProtocolVersionFlag:
		fmt.Fprintln(stdout, Version)
		return 0, true
	case printAdminAPIVersionFlag:
		fmt.Fprintln(stdout, "1")
		return 0, true
	case printStorageModeFlag:
		storageMode, err := mode.Resolve()
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1, true
		}
		fmt.Fprintln(stdout, storageMode)
		return 0, true
	default:
		return 0, false
	}
}
