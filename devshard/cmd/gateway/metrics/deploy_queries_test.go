package metrics

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
)

const observabilityDir = "../../../../deploy/join/observability"

var (
	gatewayFamilyName = regexp.MustCompile(`devshard_gateway_[a-z0-9_]+`)
	devshardctlOnly   = []string{"gonka-gateway-observability.json"}
)

// Test flow:
//  1. Collect every devshard_gateway_* family name the metrics package declares in its non-test sources.
//  2. Read every expr and query string of the dashboards under deploy/join/observability, skipping the devshardctl-only dashboard.
//  3. Assert the queries name at least one gateway family, and that each family they name, less a histogram's _bucket, _sum or _count suffix, is one the package declares.
func TestEveryShippedQueryNamesAFamilyTheGatewayDeclares(t *testing.T) {
	declared := declaredGatewayFamilies(t)
	queries := map[string][]string{}
	dashboards, err := filepath.Glob(filepath.Join(observabilityDir, "grafana", "dashboards", "*.json"))
	if err != nil || len(dashboards) == 0 {
		t.Fatalf("filepath.Glob(dashboards) = %v, %v, want at least one dashboard", dashboards, err)
	}
	for _, path := range dashboards {
		if slices.Contains(devshardctlOnly, filepath.Base(path)) {
			continue
		}
		queries[filepath.Base(path)] = dashboardQueries(t, path)
	}

	named := 0
	for file, expressions := range queries {
		for _, expression := range expressions {
			for _, family := range gatewayFamilyName.FindAllString(expression, -1) {
				named++
				if !declared[family] && !declared[histogramFamily(family)] {
					t.Errorf("%s queries %s, want a family the gateway declares in metrics/", file, family)
				}
			}
		}
	}
	if named == 0 {
		t.Fatal("gateway families named by the shipped queries = 0, want at least one")
	}
}

func declaredGatewayFamilies(t *testing.T) map[string]bool {
	t.Helper()
	sources, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("filepath.Glob(*.go) = %v, want nil", err)
	}
	declared := map[string]bool{}
	files := token.NewFileSet()
	for _, path := range sources {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(files, path, nil, 0)
		if err != nil {
			t.Fatalf("parser.ParseFile(%s) = %v, want nil", path, err)
		}
		ast.Inspect(parsed, func(node ast.Node) bool {
			literal, isLiteral := node.(*ast.BasicLit)
			if !isLiteral || literal.Kind != token.STRING {
				return true
			}
			if value, err := strconv.Unquote(literal.Value); err == nil && gatewayFamilyName.FindString(value) == value {
				declared[value] = true
			}
			return true
		})
	}
	if len(declared) == 0 {
		t.Fatal("declared gateway families = 0, want the families of metrics/")
	}
	return declared
}

func histogramFamily(series string) string {
	for _, suffix := range []string{"_bucket", "_sum", "_count"} {
		if trimmed, found := strings.CutSuffix(series, suffix); found {
			return trimmed
		}
	}
	return series
}

func dashboardQueries(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("os.ReadFile(%s) = %v, want nil", path, err)
	}
	var dashboard any
	if err := json.Unmarshal(raw, &dashboard); err != nil {
		t.Fatalf("json.Unmarshal(%s) = %v, want nil", path, err)
	}
	var queries []string
	var walk func(node any)
	walk = func(node any) {
		switch value := node.(type) {
		case map[string]any:
			for key, child := range value {
				if text, isText := child.(string); isText && (key == "expr" || key == "query") {
					queries = append(queries, text)
					continue
				}
				walk(child)
			}
		case []any:
			for _, child := range value {
				walk(child)
			}
		}
	}
	walk(dashboard)
	return queries
}
