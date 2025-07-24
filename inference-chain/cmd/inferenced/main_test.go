package main

import (
	"bytes"
	"os"
	"testing"

	svrcmd "github.com/cosmos/cosmos-sdk/server/cmd"
	"github.com/productscience/inference/app"
	"github.com/productscience/inference/cmd/inferenced/cmd"
)

func TestMain(t *testing.T) {
	tests := []struct {
		name       string
		setup      func() error
		cleanup    func()
		expectExit bool
	}{
		{
			name: "valid_command_execution",
			setup: func() error {
				// Override os.Args for valid command
				os.Args = []string{"inferenced", "--help"}
				return nil
			},
			cleanup: func() {
				// Reset os.Args to default
				os.Args = os.Args[:1]
			},
			expectExit: false,
		},
		{
			name: "default_node_home_failure",
			setup: func() error {
				// Override app node home to cause failure
				app.DefaultNodeHome = "/invalid/home"
				return nil
			},
			cleanup: func() {
				// Reset app default home
				app.DefaultNodeHome = "/valid/home"
			},
			expectExit: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Setup test-specific environment
			if tt.setup != nil {
				if err := tt.setup(); err != nil {
					t.Fatalf("setup failed: %v", err)
				}
			}

			// Redirect output to capture any errors
			var output bytes.Buffer
			rootCmd := cmd.NewRootCmd()
			rootCmd.SetOut(&output)
			rootCmd.SetErr(&output)

			// Execute rootCmd
			exitCalled := false
			exitFunc := func(code int) {
				if !tt.expectExit {
					t.Errorf("unexpected exit with code: %d", code)
				}
				exitCalled = true
			}

			// Temporarily replace os.Exit
			savedOsExit := osExit
			osExit = exitFunc
			defer func() { osExit = savedOsExit }()

			if err := svrcmd.Execute(rootCmd, "", app.DefaultNodeHome); err != nil && !tt.expectExit {
				t.Errorf("unexpected error: %v", err)
			}

			// Assert os.Exit was called when expected
			if tt.expectExit && !exitCalled {
				t.Errorf("expected exit to be called, but it wasn't")
			}

			// Cleanup test-specific environment
			if tt.cleanup != nil {
				tt.cleanup()
			}
		})
	}
}

// Placeholder for os.Exit function to allow interception in tests
var osExit = os.Exit
