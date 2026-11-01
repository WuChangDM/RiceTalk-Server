package server

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestDMRoutesWiredInRegisterAPIRoutes 是 1.9.4 私信回归的「接线守卫」。
//
// 背景：internal/dm 的 Handler/Service 一直实现齐全（含按 DM 参与者鉴权），但
// routes.go 从未 import/构造/注册它 —— 线上 GET /api/v1/dm/channels 恒 404
// SYSTEM_NOT_FOUND，私信成为死路。行为级用例在 internal/dm/dm_e2e_test.go；本
// 用例用 AST 静态断言「routes.go 里确实接上了」，防止将来重构/合并时又被删掉
// （行为用例只覆盖 dm 包自身，无法发现 routes.go 漏注册）。
//
// 断言三件事：
//  1. routes.go 导入了 "ridgericetalk/internal/dm"；
//  2. registerAPIRoutes 里用 dm.NewHandler(...) 构造了 handler；
//  3. 同一变量上调用了 RegisterRoutes(api)，且构造参数复用 a.messageService 与
//     a.hub（避免新建第二个 Hub 导致 WS 广播不互通）。
func TestDMRoutesWiredInRegisterAPIRoutes(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "routes.go", nil, 0)
	if err != nil {
		t.Fatalf("解析 routes.go: %v", err)
	}

	// 1) 导入路径。
	imported := false
	for _, imp := range file.Imports {
		if imp.Path != nil && imp.Path.Value == `"ridgericetalk/internal/dm"` {
			imported = true
			break
		}
	}
	if !imported {
		t.Fatalf("routes.go 未导入 ridgericetalk/internal/dm —— 私信路由会再次 404")
	}

	// 2) 定位 registerAPIRoutes。
	var fn *ast.FuncDecl
	for _, decl := range file.Decls {
		if f, ok := decl.(*ast.FuncDecl); ok && f.Name != nil && f.Name.Name == "registerAPIRoutes" {
			fn = f
			break
		}
	}
	if fn == nil || fn.Body == nil {
		t.Fatalf("routes.go 中找不到 registerAPIRoutes 函数体")
	}

	// 3) 收集 `xxx := dm.NewHandler(...)` 的变量名，并校验构造参数。
	dmVars := map[string]bool{}
	argChecks := map[string]bool{} // "messageService" / "hub" 是否作为实参出现
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		assign, ok := n.(*ast.AssignStmt)
		if !ok || len(assign.Lhs) != 1 || len(assign.Rhs) != 1 {
			return true
		}
		call, ok := assign.Rhs[0].(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkgIdent, ok := sel.X.(*ast.Ident)
		if !ok || pkgIdent.Name != "dm" || sel.Sel.Name != "NewHandler" {
			return true
		}
		if lhs, ok := assign.Lhs[0].(*ast.Ident); ok {
			dmVars[lhs.Name] = true
		}
		for _, arg := range call.Args {
			if a, ok := arg.(*ast.SelectorExpr); ok {
				if recv, ok := a.X.(*ast.Ident); ok && recv.Name == "a" {
					argChecks[a.Sel.Name] = true
				}
			}
		}
		return true
	})
	if len(dmVars) == 0 {
		t.Fatalf("registerAPIRoutes 中未找到 dm.NewHandler(...) 构造 —— 私信路由未注册")
	}
	if !argChecks["messageService"] {
		t.Errorf("dm.NewHandler 未复用 a.messageService（必须与 message handler 同一实例）")
	}
	if !argChecks["hub"] {
		t.Errorf("dm.NewHandler 未复用 a.hub（新建第二个 Hub 会导致 WS 广播不互通）")
	}

	// 4) 同一变量上必须在 api 组注册路由。
	wired := false
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "RegisterRoutes" || len(call.Args) != 1 {
			return true
		}
		recv, ok := sel.X.(*ast.Ident)
		if !ok || !dmVars[recv.Name] {
			return true
		}
		if arg, ok := call.Args[0].(*ast.Ident); ok && arg.Name == "api" {
			wired = true
		}
		return true
	})
	if !wired {
		t.Fatalf("dm handler 未在 registerAPIRoutes 的 api 组上调用 RegisterRoutes(api)")
	}
}
