package tests

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func getProjectRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", fmt.Errorf("repository root not found")
}

func checkContainerLogs(containerName string, requiredLogs []string) (bool, error) {
	cmd := exec.Command("docker", "logs", containerName)
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf

	output, err := cmd.Output()
	if err != nil {
		return false, err
	}

	logContent := string(output)
	foundLogs := make(map[string]bool)

	for _, log := range requiredLogs {
		if strings.Contains(logContent, log) {
			foundLogs[log] = true
		}
	}
	for _, log := range requiredLogs {
		if !foundLogs[log] {
			return false, nil
		}
	}
	return true, nil
}

func runCommand(cmd string, args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	var errBuf bytes.Buffer
	command := exec.CommandContext(ctx, cmd, args...)
	command.Stderr = &errBuf

	err := command.Run()
	if err != nil {
		fmt.Println("Command failed with error:", err)
		fmt.Println("Stderr:", errBuf.String())
	}
	return err
}

func stopContainers() error {
	fmt.Println("Stopping all running containers...")
	out, err := exec.Command("docker", "ps", "-a", "-q").Output()
	if err != nil {
		return err
	}

	ids := strings.Fields(string(out))
	args := append([]string{"stop"}, ids...)

	if err := runCommand("docker", args...); err != nil {
		return err
	}
	return nil
}

// TODO: modify scripts to be able run test in CI/CD or rewrite using testermint framework
/*
func TestCreatingAndFetchingSnapshots(t *testing.T) {
	var requiredLogs = []string{
		"Discovering snapshots",
		"Discovered new snapshot",
		"Offering snapshot to ABCI app",
		"Snapshot accepted",
		"Fetching snapshot chunk",
		"Snapshot restored",
	}

	projectRoot, err := getProjectRoot()
	assert.NoError(t, err)

	assert.NoError(t, os.Chdir(projectRoot))

	curDir, _ := os.Getwd()
	fmt.Printf("Test working dir: %s \n", curDir)

	fmt.Println("Starting local test chain...")

	scriptPath := filepath.Join(projectRoot, "launch-local-test-chain.sh")
	fmt.Printf("Script path: %s\n", scriptPath)

	assert.NoError(t, runCommand("bash", "-c", scriptPath))
	defer func() {
		assert.NoError(t, stopContainers())
	}()

	fmt.Println("Waiting for snapshots creation...")
	time.Sleep(30 * time.Second)

	fmt.Println("Running test snapshots...")
	scriptPath = filepath.Join(projectRoot, "test-snapshots.sh")
	fmt.Printf("Script path: %s\n", scriptPath)

	assert.NoError(t, runCommand("bash", "-c", scriptPath))

	fmt.Println("Waiting for snapshots fetching and applying...")
	time.Sleep(20 * time.Second)

	fmt.Println("Checking container logs for expected entries...")
	attempts := 2
	var snapshotsApplied bool
	for i := 0; i < attempts; i++ {
		snapshotsApplied, err = checkContainerLogs("join3-node", requiredLogs)
		assert.NoError(t, err)

		if snapshotsApplied {
			fmt.Println("Found snapshots logs...")
			break
		}
		fmt.Println("Logs not found, retrying in 10 seconds...")
		time.Sleep(10 * time.Second)
	}
	assert.True(t, snapshotsApplied)
}
*/
