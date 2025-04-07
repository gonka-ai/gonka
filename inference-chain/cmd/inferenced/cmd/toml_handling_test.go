package cmd

import (
	"github.com/stretchr/testify/assert"
	"testing"
)

// TestMergeTomlFiles tests merging two TOML files with comment preservation.
func TestMergeTomlFiles(t *testing.T) {
	tests := []struct {
		name       string
		toml1      string
		toml2      string
		expected   string
		shouldFail bool
	}{
		{
			name: "Merge basic files",
			toml1: `
# Database config
[db]
user = "admin"`,
			toml2: `
[db]
password = "secret"
`,
			expected: `
# Database config
[db]
user = "admin"
password = "secret"`,
		},
		{
			name: "Merge with nested tables",
			toml1: `
[server]
host = "localhost"
port = 8000

[db]
user = "admin"`,
			toml2: `
[server]
port = 8080

[db]
password = "secret"
`,
			expected: `
[server]
host = "localhost"
port = 8080

[db]
user = "admin"
password = "secret"`,
		},
		{
			name: "Merge with comments preserved in toml1",
			toml1: `
# Main server config
[server]
host = "localhost"
port = 8000

# Database config
[db]
user = "admin"`,
			toml2: `
[server]
port = 8080

[db]
password = "secret"
`,
			expected: `
# Main server config
[server]
host = "localhost"
port = 8080

# Database config
[db]
user = "admin"
password = "secret"`,
		},
		{
			name: "Merge with no changes in toml2",
			toml1: `
[server]
host = "localhost"
port = 8000
`,
			toml2: `
[server]
host = "localhost"
port = 8000
`,
			expected: `
[server]
host = "localhost"
port = 8000
`,
		},
		// Additional tests:
		{
			name: "Merge multi-line triple-quoted values",
			toml1: `
[description]
text = """This is a
multi-line value.
It has several lines."""
`,
			toml2: `
[description]
text = """Overridden
multi-line value."""
`,
			expected: `
[description]
text = """Overridden
multi-line value."""
`,
		},
		{
			name: "Merge with dramatic order differences",
			toml1: `
[global]
a = 1
b = 2

[section]
x = "foo"
y = "bar"`,
			toml2: `
[global]
b = 20
c = 30

[section]
y = "baz"
z = "qux"
`,
			expected: `
[global]
a = 1
b = 20

c = 30
[section]
x = "foo"
y = "baz"
z = "qux"`,
		},
		{
			name: "Merge global new key",
			toml1: `
a = "hello"`,
			toml2: `
a = "hello"
b = "world"
`,
			expected: `
a = "hello"
b = "world"`,
		},
		{
			name: "Merge bracket notation with dot notation",
			toml1: `
[server]
host = "localhost"
port = 8000`,
			toml2: `
server.host = "127.0.0.1"
server.port = 9000
		`,
			expected: `
[server]
host = "127.0.0.1"
port = 9000`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := MergeTomlFiles(tt.toml1, tt.toml2)
			if tt.shouldFail {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}
