package database_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"ridgericetalk/internal/database/automigrate"
)

// nonPersistedModels 列出「明确不落库、允许不出现在 AutoMigrate 清单」的 model 类型。
//
// 目前为空：internal/model 里全部 struct 都是持久化实体。
// 若将来新增纯内存 DTO，请在此登记并写明原因（同时保证它确实不是表）。
var nonPersistedModels = map[string]string{}

// TestAutoMigrateCoversAllModels 是防复发守卫（P1 事故：AutoMigrate 清单漏了
// MessageAttachment，导致 SQLite 部署下所有附件/图片消息 500 no such table）。
//
// 做法：AST 枚举 internal/model 下所有 struct 类型，再 AST 解析
// internal/database/automigrate/models.go 里 AllModels() 返回的唯一清单，做双向差集断言：
//   - model 里有、清单里没有 → 失败（SQLite 会缺表）
//   - 清单里有、model 里没有 → 失败（拼写/删类型后残留）
//   - 白名单引用了不存在的类型 → 失败（白名单腐烂）
//
// 失败信息会直接打印缺失的类型名，便于一眼修复。
func TestAutoMigrateCoversAllModels(t *testing.T) {
	modelTypes := parseModelStructs(t, filepath.Join("..", "model"))
	registered := parseAllModelsFunc(t, filepath.Join("automigrate", "models.go"))

	if len(modelTypes) == 0 {
		t.Fatal("解析 internal/model 失败：没有发现任何 struct 类型")
	}
	if len(registered) == 0 {
		t.Fatal("解析 models.go 失败：AllModels() 中没有发现 &model.X{} 项")
	}

	var missing, stale []string
	for name := range modelTypes {
		if _, ok := registered[name]; ok {
			continue
		}
		if _, allowed := nonPersistedModels[name]; allowed {
			continue
		}
		missing = append(missing, name)
	}
	for name := range registered {
		if _, ok := modelTypes[name]; !ok {
			stale = append(stale, name)
		}
	}
	for name := range nonPersistedModels {
		if _, ok := modelTypes[name]; !ok {
			stale = append(stale, "白名单:"+name)
		}
	}
	sort.Strings(missing)
	sort.Strings(stale)

	if len(missing) > 0 {
		t.Fatalf("AutoMigrate 清单缺少 %d 个 model 类型（SQLite 部署会 no such table）：%s\n"+
			"修复：在 ridgericetalk/server/internal/database/automigrate/models.go 的 AllModels() 中加入 &model.<Type>{}",
			len(missing), strings.Join(missing, ", "))
	}
	if len(stale) > 0 {
		t.Fatalf("AllModels()/白名单引用了 internal/model 中不存在的类型：%s", strings.Join(stale, ", "))
	}
}

// TestAllModelsRuntimeMatchesModelPackage 校验运行期清单与 AST 解析结果一致，
// 并确保每个元素都是非 nil 指针（防止误写成值类型导致 GORM 行为变化）。
func TestAllModelsRuntimeMatchesModelPackage(t *testing.T) {
	models := automigrate.AllModels()
	registered := parseAllModelsFunc(t, filepath.Join("automigrate", "models.go"))

	if len(models) != len(registered) {
		t.Fatalf("AllModels() 运行期长度 %d 与 AST 解析到的 %d 项不一致", len(models), len(registered))
	}
	for i, m := range models {
		if m == nil {
			t.Fatalf("AllModels()[%d] 为 nil", i)
		}
	}
}

// TestTestUtilReusesProductionAutoMigrate（task-69）防止测试库再次维护独立清单：
//   - server/tests/testutil/db.go 里不得再出现任何 &model.X{} 建表清单；
//   - 必须通过 automigrate.AllModels() 展开调用 AutoMigrate。
func TestTestUtilReusesProductionAutoMigrate(t *testing.T) {
	const testutilFile = "../../tests/testutil/db.go"
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, testutilFile, nil, 0)
	if err != nil {
		t.Fatalf("解析 %s 失败: %v", testutilFile, err)
	}

	var (
		autoMigrateCalls int
		usesAllModels    bool
		localModels      []string
	)

	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "AutoMigrate" {
			return true
		}
		autoMigrateCalls++
		for _, arg := range call.Args {
			// 期望形态：automigrate.AllModels()...
			if c, ok := arg.(*ast.CallExpr); ok {
				if s, ok := c.Fun.(*ast.SelectorExpr); ok && s.Sel.Name == "AllModels" {
					usesAllModels = true
					continue
				}
			}
			// 任何本地 &model.X{} 都视为漂移
			ast.Inspect(arg, func(inner ast.Node) bool {
				unary, ok := inner.(*ast.UnaryExpr)
				if !ok || unary.Op != token.AND {
					return true
				}
				lit, ok := unary.X.(*ast.CompositeLit)
				if !ok {
					return true
				}
				ts, ok := lit.Type.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pkg, ok := ts.X.(*ast.Ident)
				if !ok || pkg.Name != "model" {
					return true
				}
				localModels = append(localModels, ts.Sel.Name)
				return true
			})
		}
		return true
	})

	if autoMigrateCalls == 0 {
		t.Fatalf("%s 中未找到 AutoMigrate 调用（测试库建表逻辑被改动？）", testutilFile)
	}
	if len(localModels) > 0 {
		sort.Strings(localModels)
		t.Fatalf("%s 仍维护本地 AutoMigrate 清单（%d 项）：%s\n"+
			"修复：删除本地清单，改为 db.AutoMigrate(automigrate.AllModels()...)，避免与生产清单漂移（task-69）",
			testutilFile, len(localModels), strings.Join(localModels, ", "))
	}
	if !usesAllModels {
		t.Fatalf("%s 未使用 automigrate.AllModels() 建表，测试库与生产建表清单存在漂移风险（task-69）", testutilFile)
	}
}

// parseModelStructs 解析 dir 下所有非测试 .go 文件的 struct 类型名。
func parseModelStructs(t *testing.T, dir string) map[string]struct{} {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatalf("扫描 model 目录失败: %v", err)
	}
	out := make(map[string]struct{})
	fset := token.NewFileSet()
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatalf("解析 %s 失败: %v", file, err)
		}
		for _, decl := range f.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.TYPE {
				continue
			}
			for _, spec := range gen.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				if _, ok := ts.Type.(*ast.StructType); !ok {
					continue
				}
				out[ts.Name.Name] = struct{}{}
			}
		}
	}
	return out
}

// parseAllModelsFunc 解析 models.go 中 AllModels() 返回的 []interface{}{&model.X{}, ...}。
func parseAllModelsFunc(t *testing.T, file string) map[string]struct{} {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, nil, 0)
	if err != nil {
		t.Fatalf("解析 %s 失败: %v", file, err)
	}
	out := make(map[string]struct{})
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "AllModels" || fn.Body == nil {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			ret, ok := n.(*ast.ReturnStmt)
			if !ok || len(ret.Results) == 0 {
				return true
			}
			lit, ok := ret.Results[0].(*ast.CompositeLit)
			if !ok {
				return true
			}
			for _, elt := range lit.Elts {
				unary, ok := elt.(*ast.UnaryExpr)
				if !ok || unary.Op != token.AND {
					continue
				}
				inner, ok := unary.X.(*ast.CompositeLit)
				if !ok {
					continue
				}
				typeSel, ok := inner.Type.(*ast.SelectorExpr)
				if !ok {
					continue
				}
				pkg, ok := typeSel.X.(*ast.Ident)
				if !ok || pkg.Name != "model" {
					continue
				}
				out[typeSel.Sel.Name] = struct{}{}
			}
			return true
		})
	}
	return out
}
