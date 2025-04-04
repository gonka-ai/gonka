package cmd

import (
	"fmt"
	"github.com/spf13/cobra"
	"os"
	"regexp"
	"strings"

	"github.com/pelletier/go-toml"
)

func PatchToml() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "patch-toml [base-file] [override-file]",
		Short: "Merge two TOML files, with the second file's values overriding the first",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			baseFile := args[0]
			overrideFile := args[1]

			// Read base file
			baseContent, err := os.ReadFile(baseFile)
			if err != nil {
				return fmt.Errorf("error reading base file %s: %w", baseFile, err)
			}

			// Read override file
			overrideContent, err := os.ReadFile(overrideFile)
			if err != nil {
				return fmt.Errorf("error reading override file %s: %w", overrideFile, err)
			}

			// Merge the files
			result, err := MergeTomlFiles(string(baseContent), string(overrideContent))
			if err != nil {
				return fmt.Errorf("error merging TOML files: %w", err)
			}

			// Write the result back to the base file
			if err := os.WriteFile(baseFile, []byte(result), 0644); err != nil {
				return fmt.Errorf("error writing merged content to %s: %w", baseFile, err)
			}

			fmt.Printf("Successfully merged %s into %s\n", overrideFile, baseFile)
			return nil
		},
	}
	return cmd
}

// Below here, All AI generated (FWIW, openAI o3-mini-high, with a once over by Claude 3.7 Sonnet.
// But tested

// MergeTomlFiles patches toml1 (the base) with values from toml2 (the override).
// toml1's comments and formatting are preserved; keys in toml2 override those in toml1.
// Any keys in toml2 not found in toml1 are appended at the end of their section.
// Note: Multi-line values are detected for triple-quoted strings.
func MergeTomlFiles(toml1, toml2 string) (string, error) {
	// Parse toml2 into a tree (we ignore its comments)
	overrideTree, err := toml.Load(toml2)
	if err != nil {
		return "", fmt.Errorf("failed to parse toml2: %w", err)
	}

	// Track which keys in each section have been processed.
	usedOverrides := make(map[string]map[string]bool)
	usedOverrides[""] = make(map[string]bool)
	// For any override section, initialize the tracking map.
	for _, sec := range overrideTree.Keys() {
		if sub := overrideTree.Get(sec); sub != nil {
			if _, ok := sub.(*toml.Tree); ok {
				usedOverrides[sec] = make(map[string]bool)
			}
		}
	}

	// Regular expressions:
	// section header: e.g., [server]
	sectionRegex := regexp.MustCompile(`^\s*\[([^\]]+)\]\s*$`)
	// key-value assignment: e.g., key = value
	kvRegex := regexp.MustCompile(`^\s*([^=\s]+)\s*=\s*(.+)$`)

	lines := strings.Split(toml1, "\n")
	var outLines []string
	currentSection := "" // empty string for global keys

	// appendMissingOverrides appends any override keys for the given section that
	// have not yet been output.
	appendMissingOverrides := func(section string) {
		var keys []string
		if section == "" {
			// Global keys: in overrideTree, keys whose value is not a table.
			for _, k := range overrideTree.Keys() {
				if overrideTree.Get(k) != nil {
					if _, isTree := overrideTree.Get(k).(*toml.Tree); !isTree {
						if !usedOverrides[""][k] {
							keys = append(keys, k)
						}
					}
				}
			}
		} else {
			secVal := overrideTree.Get(currentSection)
			if subtree, ok := secVal.(*toml.Tree); ok {
				for _, k := range subtree.Keys() {
					if !usedOverrides[currentSection][k] {
						keys = append(keys, k)
					}
				}
			}
		}
		for _, k := range keys {
			if line, ok := getOverrideLine(overrideTree, section, k, usedOverrides); ok {
				outLines = append(outLines, line)
			}
		}
	}

	i := 0
	for i < len(lines) {
		rawLine := lines[i]
		line := strings.TrimSpace(rawLine)
		// Check for section header.
		if matches := sectionRegex.FindStringSubmatch(line); matches != nil {
			// Before switching sections, append any missing keys for the current section.
			appendMissingOverrides(currentSection)
			currentSection = strings.TrimSpace(matches[1])
			outLines = append(outLines, rawLine)
			i++
			continue
		}

		// Check for a key-value assignment.
		if matches := kvRegex.FindStringSubmatch(line); matches != nil {
			key := matches[1]
			valuePart := matches[2]
			if overrideLine, ok := getOverrideLine(overrideTree, currentSection, key, usedOverrides); ok {
				// If the value looks like a triple-quoted multi-line value, skip its subsequent lines.
				trimVal := strings.TrimSpace(valuePart)
				if strings.HasPrefix(trimVal, `"""`) {
					i++ // skip the current line
					for i < len(lines) {
						if strings.Contains(lines[i], `"""`) {
							i++ // skip the closing line too
							break
						}
						i++
					}
				} else {
					i++ // single-line value; skip it.
				}
				outLines = append(outLines, overrideLine)
				continue
			}
		}
		// Default: output the current line unchanged.
		outLines = append(outLines, rawLine)
		i++
	}
	// Append missing overrides for the final section.
	appendMissingOverrides(currentSection)

	// Also, if there are entire override sections not in toml1, append them.
	for _, sec := range overrideTree.Keys() {
		// If override value is a table, check if that section exists in toml1.
		if subtree, ok := overrideTree.Get(sec).(*toml.Tree); ok {
			sectionHeader := "[" + sec + "]"
			found := false
			for _, ol := range outLines {
				if strings.TrimSpace(ol) == sectionHeader {
					found = true
					break
				}
			}
			if !found {
				outLines = append(outLines, "", sectionHeader)
				// Append every key from that section.
				for _, subKey := range subtree.Keys() {
					if line, ok := getOverrideLine(overrideTree, sec, subKey, usedOverrides); ok {
						outLines = append(outLines, line)
					}
				}
			}
		}
	}

	return strings.Join(outLines, "\n"), nil
}

// getOverrideLine checks if overrideTree has an override for key in section.
// If so, it marshals a temporary map { key: value } and marks the key as used.
func getOverrideLine(overrideTree *toml.Tree, section, key string, usedOverrides map[string]map[string]bool) (string, bool) {
	var val interface{}
	if section == "" {
		val = overrideTree.Get(key)
		if val == nil {
			return "", false
		}
		usedOverrides[""][key] = true
	} else {
		secVal := overrideTree.Get(section)
		if subtree, ok := secVal.(*toml.Tree); ok {
			val = subtree.Get(key)
			if val == nil {
				return "", false
			}
			// Mark as used.
			if usedOverrides[section] == nil {
				usedOverrides[section] = make(map[string]bool)
			}
			usedOverrides[section][key] = true
		} else {
			return "", false
		}
	}

	// If the value is a string with a newline, output it as a triple-quoted string.
	if s, ok := val.(string); ok && strings.Contains(s, "\n") {
		// Format as: key = """<value>"""
		return fmt.Sprintf("%s = \"\"\"%s\"\"\"", key, s), true
	}

	// Create a temporary map to marshal just this key/value.
	tempMap := map[string]interface{}{key: val}
	marshaled, err := toml.Marshal(tempMap)
	if err != nil {
		return "", false
	}
	// toml.Marshal produces a trailing newline; remove it.
	return strings.TrimSuffix(string(marshaled), "\n"), true
}
