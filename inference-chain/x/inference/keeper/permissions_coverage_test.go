package keeper_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/productscience/inference/x/inference/keeper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type handlerMeta struct {
	filePath         string
	msgType          string
	checkPermissions []string
	hasCheck         bool
}

// It's kind of brute force, but this asserts that we have handlers for all messages
// and that each handler calls CheckPermission and matches the table in permissions.go
func TestTxProtoMsgHandlersUseUnifiedPermissions(t *testing.T) {
	protoRPCs := parseMsgServiceRPCs(t)
	handlers := parseMsgServerHandlers(t)
	mapPerms := permissionsMapFromKeeper()

	for methodName, msgType := range protoRPCs {
		h, ok := handlers[methodName]
		assert.Truef(t, ok, "missing msgServer handler for rpc %s(%s)", methodName, msgType)
		assert.Equalf(t, msgType, h.msgType, "handler %s uses wrong msg type", methodName)
		assert.Truef(t, strings.HasPrefix(filepath.Base(h.filePath), "msg_server_"), "handler %s must be in msg_server_*.go, found %s", methodName, h.filePath)
		assert.Truef(t, h.hasCheck, "handler %s is missing CheckPermission call", methodName)

		perms, ok := mapPerms[msgType]
		assert.Truef(t, ok, "permissions.go is missing MessagePermissions entry for %s", msgType)
		assert.Equalf(t, sorted(perms), sorted(h.checkPermissions), "permissions mismatch for %s", methodName)
	}
}

func parseMsgServiceRPCs(t *testing.T) map[string]string {
	t.Helper()
	protoPath := filepath.Join("..", "..", "..", "proto", "inference", "inference", "tx.proto")
	bz, err := os.ReadFile(protoPath)
	require.NoError(t, err)
	re := regexp.MustCompile(`rpc\s+([A-Za-z0-9_]+)\s*\(\s*([A-Za-z0-9_]+)\s*\)\s*returns`)
	matches := re.FindAllStringSubmatch(string(bz), -1)
	require.NotEmpty(t, matches)
	out := make(map[string]string, len(matches))
	for _, m := range matches {
		out[m[1]] = m[2]
	}
	return out
}

func parseMsgServerHandlers(t *testing.T) map[string]handlerMeta {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, ".", func(info os.FileInfo) bool {
		return strings.HasSuffix(info.Name(), ".go") && !strings.HasSuffix(info.Name(), "_test.go")
	}, 0)
	require.NoError(t, err)

	pkg, ok := pkgs["keeper"]
	require.True(t, ok)

	out := make(map[string]handlerMeta)
	for filePath, fileAST := range pkg.Files {
		for _, decl := range fileAST.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Recv == nil || fd.Name == nil || fd.Type == nil || fd.Type.Params == nil {
				continue
			}

			recvType, ok := recvMsgServer(fd.Recv.List)
			if !ok || recvType != "msgServer" {
				continue
			}

			second, ok := paramTypeAt(fd.Type.Params.List, 1)
			if !ok {
				continue
			}
			msgType, ok := secondParamMsgType(second)
			if !ok {
				continue
			}

			meta := handlerMeta{
				filePath: filePath,
				msgType:  msgType,
			}
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel == nil || sel.Sel.Name != "CheckPermission" {
					return true
				}
				meta.hasCheck = true
				for _, arg := range call.Args[2:] {
					if id, ok := arg.(*ast.Ident); ok {
						meta.checkPermissions = append(meta.checkPermissions, permissionIdentToValue(id.Name))
					}
				}
				return false
			})

			out[fd.Name.Name] = meta
		}
	}
	return out
}

func recvMsgServer(fields []*ast.Field) (string, bool) {
	if len(fields) != 1 {
		return "", false
	}
	switch t := fields[0].Type.(type) {
	case *ast.Ident:
		return t.Name, true
	case *ast.StarExpr:
		if id, ok := t.X.(*ast.Ident); ok {
			return id.Name, true
		}
	}
	return "", false
}

func secondParamMsgType(expr ast.Expr) (string, bool) {
	star, ok := expr.(*ast.StarExpr)
	if !ok {
		return "", false
	}
	sel, ok := star.X.(*ast.SelectorExpr)
	if !ok {
		return "", false
	}
	if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "types" {
		return "", false
	}
	return sel.Sel.Name, true
}

func paramTypeAt(fields []*ast.Field, idx int) (ast.Expr, bool) {
	count := 0
	for _, f := range fields {
		names := len(f.Names)
		if names == 0 {
			names = 1
		}
		if idx < count+names {
			return f.Type, true
		}
		count += names
	}
	return nil, false
}

func permissionsMapFromKeeper() map[string][]string {
	out := make(map[string][]string)
	for msgType, perms := range keeper.MessagePermissions {
		msgName := msgType.Elem().Name()
		names := make([]string, 0, len(perms))
		for _, p := range perms {
			names = append(names, reflect.ValueOf(p).String())
		}
		out[msgName] = names
	}
	return out
}

func permissionIdentToValue(name string) string {
	switch name {
	case "GovernancePermission":
		return string(keeper.GovernancePermission)
	case "TrainingExecPermission":
		return string(keeper.TrainingExecPermission)
	case "TrainingStartPermission":
		return string(keeper.TrainingStartPermission)
	case "ParticipantPermission":
		return string(keeper.ParticipantPermission)
	case "ActiveParticipantPermission":
		return string(keeper.ActiveParticipantPermission)
	case "AccountPermission":
		return string(keeper.AccountPermission)
	case "CurrentActiveParticipantPermission":
		return string(keeper.CurrentActiveParticipantPermission)
	case "ContractPermission":
		return string(keeper.ContractPermission)
	case "NoPermission":
		return string(keeper.NoPermission)
	case "PreviousActiveParticipantPermission":
		return string(keeper.PreviousActiveParticipantPermission)
	case "OpenRegistrationPermission":
		return string(keeper.OpenRegistrationPermission)
	case "EscrowAllowListPermission":
		return string(keeper.EscrowAllowListPermission)
	default:
		return name
	}
}

func sorted(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}
