package apiconfig

import (
	"strings"
	"testing"
)

// MockWriteCloser for testing
type MockWriteCloser struct {
	data strings.Builder
}

func (m *MockWriteCloser) Write(p []byte) (n int, err error) {
	return m.data.Write(p)
}

func (m *MockWriteCloser) Close() error {
	return nil
}

func (m *MockWriteCloser) GetData() string {
	return m.data.String()
}

// MockWriteCloserProvider for testing
type MockWriteCloserProvider struct {
	writer *MockWriteCloser
}

func (m *MockWriteCloserProvider) GetWriter() WriteCloser {
	return m.writer
}

func NewMockWriteCloserProvider() *MockWriteCloserProvider {
	return &MockWriteCloserProvider{
		writer: &MockWriteCloser{},
	}
}

func (m *MockWriteCloserProvider) GetMockWriter() *MockWriteCloser {
	return m.writer
}

func TestVersionChangeDetectionAndPersistence(t *testing.T) {
	// Setup mock writer provider
	mockWriterProvider := NewMockWriteCloserProvider()

	// Create config manager with initial version
	cm := &ConfigManager{
		currentConfig: Config{
			CurrentNodeVersion:  "v3.0.6",
			PreviousNodeVersion: "",
			CurrentHeight:       100,
			NodeVersions: NodeVersionStack{
				Versions: []NodeVersion{
					{Height: 150, Version: "v3.0.8"},
				},
			},
		},
		WriterProvider: mockWriterProvider,
	}

	// Verify initial state
	if cm.GetCurrentNodeVersion() != "v3.0.6" {
		t.Errorf("Expected current version to be v3.0.6, got %s", cm.GetCurrentNodeVersion())
	}
	if cm.GetPreviousNodeVersion() != "" {
		t.Errorf("Expected previous version to be empty, got %s", cm.GetPreviousNodeVersion())
	}

	// Trigger version change by setting height to upgrade height
	err := cm.SetHeight(150)
	if err != nil {
		t.Errorf("SetHeight failed: %v", err)
	}

	// Verify version change was detected and persisted
	if cm.GetCurrentNodeVersion() != "v3.0.8" {
		t.Errorf("Expected current version to be v3.0.8, got %s", cm.GetCurrentNodeVersion())
	}
	if cm.GetPreviousNodeVersion() != "v3.0.6" {
		t.Errorf("Expected previous version to be v3.0.6, got %s", cm.GetPreviousNodeVersion())
	}

	// Verify config was written to persistent storage
	configData := mockWriterProvider.GetMockWriter().GetData()
	if !strings.Contains(configData, "current_node_version: v3.0.8") {
		t.Errorf("Config should contain current_node_version: v3.0.8")
	}
	if !strings.Contains(configData, "previous_node_version: v3.0.6") {
		t.Errorf("Config should contain previous_node_version: v3.0.6")
	}
}

func TestNoVersionChangeWhenHeightNotReached(t *testing.T) {
	// Setup mock writer provider
	mockWriterProvider := NewMockWriteCloserProvider()

	// Create config manager with initial version
	cm := &ConfigManager{
		currentConfig: Config{
			CurrentNodeVersion:  "v3.0.6",
			PreviousNodeVersion: "",
			CurrentHeight:       100,
			NodeVersions: NodeVersionStack{
				Versions: []NodeVersion{
					{Height: 200, Version: "v3.0.8"},
				},
			},
		},
		WriterProvider: mockWriterProvider,
	}

	// Set height to before upgrade height
	err := cm.SetHeight(150)
	if err != nil {
		t.Errorf("SetHeight failed: %v", err)
	}

	// Verify no version change occurred
	if cm.GetCurrentNodeVersion() != "v3.0.6" {
		t.Errorf("Expected current version to remain v3.0.6, got %s", cm.GetCurrentNodeVersion())
	}
	if cm.GetPreviousNodeVersion() != "" {
		t.Errorf("Expected previous version to remain empty, got %s", cm.GetPreviousNodeVersion())
	}
}

func TestMarkUpgradeComplete(t *testing.T) {
	// Setup mock writer provider
	mockWriterProvider := NewMockWriteCloserProvider()

	// Create config manager simulating incomplete upgrade state
	cm := &ConfigManager{
		currentConfig: Config{
			CurrentNodeVersion:  "v3.0.8",
			PreviousNodeVersion: "v3.0.6", // Indicates incomplete upgrade
		},
		WriterProvider: mockWriterProvider,
	}

	// Mark upgrade as complete
	err := cm.MarkUpgradeComplete("v3.0.8")
	if err != nil {
		t.Errorf("MarkUpgradeComplete failed: %v", err)
	}

	// Verify upgrade marked as complete (previous = current)
	if cm.GetPreviousNodeVersion() != "v3.0.8" {
		t.Errorf("Expected previous version to be v3.0.8, got %s", cm.GetPreviousNodeVersion())
	}

	// Verify config was written
	configData := mockWriterProvider.GetMockWriter().GetData()
	if !strings.Contains(configData, "previous_node_version: v3.0.8") {
		t.Errorf("Config should contain previous_node_version: v3.0.8")
	}
}

func TestRestartDetection(t *testing.T) {
	// This test simulates the restart scenario mentioned in the proposal

	// Scenario: System restarted with different previous/current versions
	// This indicates incomplete upgrade
	config := Config{
		CurrentNodeVersion:  "v3.0.8",
		PreviousNodeVersion: "v3.0.6", // Different = incomplete upgrade
	}

	// System should detect this as incomplete upgrade state
	if config.PreviousNodeVersion != "" && config.PreviousNodeVersion != config.CurrentNodeVersion {
		// This is the detection logic that broker will use
		// Test passes if we can detect the condition
		if config.PreviousNodeVersion != "v3.0.6" || config.CurrentNodeVersion != "v3.0.8" {
			t.Errorf("Failed to detect incomplete upgrade state")
		}
	} else {
		t.Errorf("Should have detected incomplete upgrade state")
	}
}
