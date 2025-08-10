package main

import (
	"os"
	"testing"
)

func TestMainFunc(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("main() panicked: %v", r)
		}
	}()
	os.Args = []string{"cmd/inferenced", "version"}
	main()
}
