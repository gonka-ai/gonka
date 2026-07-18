package hostctl

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

type Remote interface {
	Run(context.Context, string, ...string) (string, error)
}

type SSHRemote struct {
	Binary  string
	Options []string
}

var defaultSSHOptions = []string{
	"-o", "BatchMode=yes",
	"-o", "ConnectTimeout=10",
	"-o", "ServerAliveInterval=5",
	"-o", "ServerAliveCountMax=3",
}

func (r SSHRemote) Run(ctx context.Context, destination string, args ...string) (string, error) {
	if destination == "" || destination == "local" {
		if len(args) == 0 {
			return "", fmt.Errorf("local remote command is empty")
		}
		return runCommand(ctx, args[0], args[1:]...)
	}
	binary := r.Binary
	if binary == "" {
		binary = "ssh"
	}
	remoteCommand := make([]string, 0, len(args))
	for _, arg := range args {
		remoteCommand = append(remoteCommand, shellQuote(arg))
	}
	sshArgs := append([]string(nil), r.Options...)
	sshArgs = append(sshArgs, defaultSSHOptions...)
	sshArgs = append(sshArgs, destination, "--", strings.Join(remoteCommand, " "))
	return runCommand(ctx, binary, sshArgs...)
}

func runCommand(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return string(output), fmt.Errorf("%s: %w: %s", name, err, output)
	}
	return string(output), nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
