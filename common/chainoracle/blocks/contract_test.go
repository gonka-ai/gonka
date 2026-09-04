package blocks

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// TestBlocksInterfaceGolden records the BlockOracle method set. Any
// change requires updating testdata/blocks_interface_golden.txt.
func TestBlocksInterfaceGolden(t *testing.T) {
	iface := reflect.TypeFor[BlockOracle]()
	if iface.Kind() != reflect.Interface {
		t.Fatalf("expected interface, got %s", iface.Kind())
	}
	names := make([]string, 0, iface.NumMethod())
	for i := range iface.NumMethod() {
		names = append(names, iface.Method(i).Name)
	}
	sort.Strings(names)
	got := strings.Join(names, "\n") + "\n"

	_, thisFile, _, _ := runtime.Caller(0)
	dir := filepath.Dir(thisFile)
	goldenPath := filepath.Join(dir, "testdata", "blocks_interface_golden.txt")
	wantBytes, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("read golden: %v", err)
	}
	want := string(wantBytes)
	if want != got {
		t.Fatalf("BlockOracle method set changed.\n--- want (golden) ---\n%s--- got ---\n%s", want, got)
	}
}
