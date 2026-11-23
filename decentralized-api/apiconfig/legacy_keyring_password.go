package apiconfig

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

func removeLegacyKeyringPassword(configPath string) error {
	original, err := os.Lstat(configPath)
	if err != nil {
		return fmt.Errorf("inspect static config: %w", err)
	}
	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("read static config: %w", err)
	}

	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		// Parser errors can contain text from the configuration.
		return errors.New("parse static config")
	}

	root := document.Content[0]
	chainNodeIndex := mappingKeyIndex(root, "chain_node")
	if chainNodeIndex == -1 {
		return nil
	}
	chainNode := root.Content[chainNodeIndex+1]
	passwordIndex := mappingKeyIndex(chainNode, "KeyringPassword")
	if passwordIndex == -1 {
		return nil
	}
	if !original.Mode().IsRegular() {
		return errors.New("static config is not a regular file")
	}
	if err := decoder.Decode(&yaml.Node{}); !errors.Is(err, io.EOF) {
		return errors.New("static config does not contain exactly one YAML document")
	}

	chainNode.Content = append(chainNode.Content[:passwordIndex], chainNode.Content[passwordIndex+2:]...)
	cleaned, err := yaml.Marshal(&document)
	if err != nil {
		return errors.New("encode cleaned static config")
	}
	// A removed value may define an anchor used by another setting.
	if err := yaml.Unmarshal(cleaned, &yaml.Node{}); err != nil {
		return errors.New("validate cleaned static config")
	}

	return replaceStaticConfig(configPath, original, cleaned)
}

func mappingKeyIndex(node *yaml.Node, key string) int {
	if node.Kind != yaml.MappingNode {
		return -1
	}
	for index := 0; index < len(node.Content); index += 2 {
		candidate := node.Content[index]
		if candidate.Kind == yaml.ScalarNode && candidate.Value == key {
			return index
		}
	}
	return -1
}

func replaceStaticConfig(configPath string, original os.FileInfo, data []byte) error {
	temp, err := os.CreateTemp(filepath.Dir(configPath), ".api-config-cleanup-*")
	if err != nil {
		return fmt.Errorf("create temporary config: %w", err)
	}
	defer os.Remove(temp.Name())
	defer temp.Close()

	if _, err := temp.Write(data); err != nil {
		return fmt.Errorf("write temporary config: %w", err)
	}
	if err := temp.Chmod(original.Mode().Perm()); err != nil {
		return fmt.Errorf("set temporary config permissions: %w", err)
	}
	if err := temp.Sync(); err != nil {
		return fmt.Errorf("sync temporary config: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close temporary config: %w", err)
	}

	current, err := os.Lstat(configPath)
	if err != nil {
		return fmt.Errorf("recheck static config: %w", err)
	}
	if !os.SameFile(original, current) || original.Size() != current.Size() ||
		!original.ModTime().Equal(current.ModTime()) || original.Mode() != current.Mode() {
		return errors.New("static config changed during cleanup")
	}
	if err := os.Rename(temp.Name(), configPath); err != nil {
		return fmt.Errorf("replace static config: %w", err)
	}
	return nil
}
