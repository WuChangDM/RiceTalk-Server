package database

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"

	"gorm.io/gorm/schema"

	"ridgericetalk/internal/database/automigrate"
)

// 1.9.4 生产事故预防：迁移里的表名必须等于 GORM 默认表名。
//
// 背景（真实缺陷，1.9.4 复审发现）：`000045_sharedoc_collaborators` 初版把表建成
// `sharedoc_collaborators`，而 GORM 对 `SharedDocumentCollaborator` 推导出的表名是
// `shared_document_collaborators`。生产 `RRT_ENV=production` 默认关闭 AutoMigrate，
// 库里只有迁移建的表 → 代码所有协作者查询 `no such table`：editor PATCH 与
// `GET /sharedoc/{id}/editors` 直接 500，而 owner 自己 PATCH 不查表看起来正常。
// SQLite 单测因为 AutoMigrate 按 GORM 名建表，永远测不出来（同 000044 的
// TIMESTAMP/TIMESTAMPTZ 那类「SQLite 绿、PG 红」的坑）。
//
// 因此这里做双向守卫：
//  1. 针对性：000045 的表名/回滚表名必须等于 GORM 表名；000046 必须把两张历史
//     错名表（minigame_leaderboard / e2ee_keys）原地改名为 GORM 名；
//  2. 通用：所有迁移文件里 CREATE TABLE 的表名必须是某个已知模型的 GORM 表名
//     （防止再写错名；纯 SQL 无模型的表走 allowlist 显式登记；历史错名表若在
//     后续迁移里被 ALTER TABLE ... RENAME TO 改成 GORM 名，也算合法）。

// migrationTableAllowlist 登记「迁移建表但没有同名 GORM 模型表」的表，附原因。
// 两类合法/已登记情形：
//   - 历史遗留、代码里已无模型/无引用的表（保留仅为兼容旧库）；
//   - **已知不一致缺陷**：迁移名与模型 GORM 名不同（生产关闭 AutoMigrate 时会
//     no such table），已登记待另立迁移修复，修复后必须从本表移除。
var migrationTableAllowlist = map[string]string{
	"mfa_secrets":         "历史遗留 MFA 表：代码中无对应模型、无 GORM 引用，保留仅为兼容旧库",
	"totp_recovery_codes": "历史遗留 TOTP 恢复码表：代码中无对应模型、无 GORM 引用",
	// 说明：e2ee_keys 与 minigame_leaderboard 两条「已知不一致」豁免已由 000046
	// rename 迁移修复，故从本表移除；它们现在以「被改名旧名」的形式由
	// renamedTables() 放行（见 TestAllMigrationTablesMatchGormModelNames）。
}

// modelTableNames 返回 automigrate.AllModels() 全部模型 → GORM 表名的映射，
// 优先使用模型显式实现的 TableName()（如 PasswordHistory）。
func modelTableNames(t *testing.T) map[string]string {
	t.Helper()
	strategy := schema.NamingStrategy{}
	out := make(map[string]string, len(automigrate.AllModels()))
	for _, m := range automigrate.AllModels() {
		typ := reflect.TypeOf(m)
		if typ == nil || typ.Kind() != reflect.Ptr {
			t.Fatalf("AllModels 元素应为指针类型，实际 %T", m)
		}
		name := typ.Elem().Name()
		table := strategy.TableName(name)
		if tb, ok := m.(interface{ TableName() string }); ok {
			table = tb.TableName()
		}
		out[table] = name
	}
	return out
}

func migrationFilePath(t *testing.T, name string) string {
	t.Helper()
	_, sourcePath, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(sourcePath), "../../../migrations", name)
}

var createTableRe = regexp.MustCompile(`(?i)CREATE TABLE (?:IF NOT EXISTS )?"?([A-Za-z0-9_]+)"?`)

// renameTableRe 匹配「ALTER TABLE ... RENAME TO ...」形式的表改名（000046 起使用）。
var renameTableRe = regexp.MustCompile(`(?i)ALTER TABLE (?:IF EXISTS )?"?([A-Za-z0-9_]+)"?\s+RENAME TO\s+"?([A-Za-z0-9_]+)"?`)

// renamedTables 汇总所有 up 迁移里的「旧表名 → 新表名」改名，供通用守卫放行
// 「历史迁移建过错名表、但已被后续迁移改正」的情形（000046 的两张表即此类）。
func renamedTables(t *testing.T) map[string]string {
	t.Helper()
	files, err := filepath.Glob(migrationFilePath(t, "*.up.sql"))
	if err != nil {
		t.Fatalf("枚举 migrations 失败: %v", err)
	}
	out := make(map[string]string)
	for _, path := range files {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读取 %s 失败: %v", filepath.Base(path), err)
		}
		for _, m := range renameTableRe.FindAllStringSubmatch(string(content), -1) {
			out[m[1]] = m[2]
		}
	}
	return out
}

// TestSharedDocumentCollaboratorMigrationMatchesGormTableName 针对 000045 的回归守卫。
func TestSharedDocumentCollaboratorMigrationMatchesGormTableName(t *testing.T) {
	tables := modelTableNames(t)
	strategy := schema.NamingStrategy{}
	expected := strategy.TableName("SharedDocumentCollaborator")
	if expected != "shared_document_collaborators" {
		t.Fatalf("GORM 表名推导异常：期望 shared_document_collaborators，实际 %q", expected)
	}
	if _, exists := tables[expected]; !exists {
		t.Fatalf("automigrate.AllModels() 里找不到表 %q 对应的模型 SharedDocumentCollaborator", expected)
	}

	for _, file := range []string{
		"000045_sharedoc_collaborators.up.sql",
		"000045_sharedoc_collaborators.down.sql",
	} {
		content, err := os.ReadFile(migrationFilePath(t, file))
		if err != nil {
			t.Fatalf("读取迁移 %s 失败: %v", file, err)
		}
		found := createTableRe.FindAllStringSubmatch(string(content), -1)
		if strings.Contains(file, ".down.") {
			if !strings.Contains(string(content), "DROP TABLE IF EXISTS "+expected) {
				t.Errorf("%s 必须 DROP TABLE IF EXISTS %s（与 up 迁移同名），否则回滚后残留错名表", file, expected)
			}
			continue
		}
		if len(found) == 0 {
			t.Fatalf("%s 未找到 CREATE TABLE 语句", file)
		}
		for _, m := range found {
			if m[1] != expected {
				t.Errorf("迁移表名必须等于 GORM 表名，否则 PG 部署 no such table：%s 建的是 %q，GORM 期望 %q（模型 SharedDocumentCollaborator）", file, m[1], expected)
			}
		}
	}
}

// TestAllMigrationTablesMatchGormModelNames 通用守卫：迁移建的表必须能被某个模型访问到。
func TestAllMigrationTablesMatchGormModelNames(t *testing.T) {
	tables := modelTableNames(t)
	files, err := filepath.Glob(migrationFilePath(t, "*.up.sql"))
	if err != nil {
		t.Fatalf("枚举 migrations 失败: %v", err)
	}
	if len(files) == 0 {
		t.Fatalf("migrations 目录没有 *.up.sql")
	}
	sort.Strings(files)

	seen := make(map[string]string) // 表名 → 首个出现它的迁移文件
	for _, path := range files {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读取 %s 失败: %v", filepath.Base(path), err)
		}
		for _, m := range createTableRe.FindAllStringSubmatch(string(content), -1) {
			name := m[1]
			if _, ok := seen[name]; ok {
				continue
			}
			seen[name] = filepath.Base(path)
		}
	}

	unknown := make([]string, 0)
	renamed := renamedTables(t)
	for table, file := range seen {
		if _, ok := tables[table]; ok {
			continue
		}
		// 历史错名表：若后续迁移把它 RENAME TO 成某个模型的 GORM 表名，则放行。
		if target, ok := renamed[table]; ok {
			if _, ok := tables[target]; ok {
				continue
			}
			unknown = append(unknown, table+" (首次出现在 "+file+"，被改名为 "+target+"，但 "+target+" 不是任何模型的 GORM 表名)")
			continue
		}
		if reason, allowed := migrationTableAllowlist[table]; allowed {
			_ = reason
			continue
		}
		unknown = append(unknown, table+" (首次出现在 "+file+")")
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		t.Errorf("迁移建了以下表，但 automigrate.AllModels() 中没有任何模型推导出同名 GORM 表：\n  %s\n"+
			"后果：生产 RRT_ENV=production 关闭 AutoMigrate 时，代码按 GORM 名查询会 no such table（SQLite 测不出来）。\n"+
			"修法：迁移表名改成模型的 GORM 默认名（或给模型加 TableName() 并在迁移里用同名），"+
			"纯 SQL/历史遗留表登记到 migrationTableAllowlist 并写明原因。",
			strings.Join(unknown, "\n  "))
	}
}

// TestMigrationTableAllowlistEntriesStillExist 允许清单必须仍然对应迁移里真实存在的表：
// 表被改名/删除后要同步清理条目，避免「豁免」长期掩盖新问题。
func TestMigrationTableAllowlistEntriesStillExist(t *testing.T) {
	files, err := filepath.Glob(migrationFilePath(t, "*.up.sql"))
	if err != nil || len(files) == 0 {
		t.Fatalf("枚举 migrations 失败: %v", err)
	}
	seen := make(map[string]bool)
	for _, path := range files {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("读取 %s 失败: %v", filepath.Base(path), err)
		}
		for _, m := range createTableRe.FindAllStringSubmatch(string(content), -1) {
			seen[m[1]] = true
		}
	}
	for table, reason := range migrationTableAllowlist {
		if !seen[table] {
			t.Errorf("migrationTableAllowlist 条目 %q 在 migrations 里已不存在，请删除该豁免（原因：%s）", table, reason)
		}
	}
}

// TestTableNameAlignMigrationRenamesLegacyTables 守卫 000046：两张历史错名表必须由
// 版本化迁移原地改名成 GORM 默认表名。
//
// 为什么必须是 rename 而不是改模型 TableName()：生产 RRT_ENV=production 关闭
// AutoMigrate，PG 只认迁移建的表；把模型改成旧名会让 SQLite/AutoMigrate 新建旧名表、
// 已存在的 GORM 名表变孤儿数据。rename 只换表名不迁移数据，行数据不丢；SQLite 部署
// 不跑版本化迁移（AutoMigrate 本来就用 GORM 名），不受影响。
func TestTableNameAlignMigrationRenamesLegacyTables(t *testing.T) {
	tables := modelTableNames(t)
	strategy := schema.NamingStrategy{}
	cases := []struct {
		model    string
		oldTable string
		newTable string
	}{
		{"MinigameLeaderboard", "minigame_leaderboard", "minigame_leaderboards"},
		{"E2EEKey", "e2ee_keys", "e2_ee_keys"},
	}
	for _, c := range cases {
		if got := strategy.TableName(c.model); got != c.newTable {
			t.Fatalf("GORM 表名推导异常：%s 期望 %q，实际 %q", c.model, c.newTable, got)
		}
		if _, ok := tables[c.newTable]; !ok {
			t.Fatalf("automigrate.AllModels() 里找不到表 %q 对应的模型 %s", c.newTable, c.model)
		}
		if reason, ok := migrationTableAllowlist[c.oldTable]; ok {
			t.Errorf("表 %q 已由 000046 改名修复，必须从 migrationTableAllowlist 移除（原豁免原因：%s）", c.oldTable, reason)
		}
	}

	const upFile = "000046_table_names_align_gorm.up.sql"
	const downFile = "000046_table_names_align_gorm.down.sql"
	upRaw, err := os.ReadFile(migrationFilePath(t, upFile))
	if err != nil {
		t.Fatalf("读取迁移 %s 失败: %v", upFile, err)
	}
	downRaw, err := os.ReadFile(migrationFilePath(t, downFile))
	if err != nil {
		t.Fatalf("读取迁移 %s 失败: %v", downFile, err)
	}
	upText, downText := string(upRaw), string(downRaw)

	for _, c := range cases {
		wantUp := "ALTER TABLE IF EXISTS " + c.oldTable + " RENAME TO " + c.newTable
		if !strings.Contains(upText, wantUp) {
			t.Errorf("%s 缺少 %q：PG 存量库必须原地改名，否则生产查询 no such table", upFile, wantUp)
		}
		wantDown := "ALTER TABLE IF EXISTS " + c.newTable + " RENAME TO " + c.oldTable
		if !strings.Contains(downText, wantDown) {
			t.Errorf("%s 缺少 %q：回滚必须把表名改回历史名", downFile, wantDown)
		}
	}

	// 约束/索引名同步对齐（PG 的 RENAME 会让索引自动跟随表 OID，但旧前缀名字会造成困扰）
	for _, want := range []string{
		"RENAME CONSTRAINT minigame_leaderboard_game_type_user_id_key TO minigame_leaderboards_game_type_user_id_key",
		"ALTER INDEX idx_minigame_leaderboard_game_score RENAME TO idx_minigame_leaderboards_game_score",
		"ALTER INDEX idx_minigame_leaderboard_user RENAME TO idx_minigame_leaderboards_user",
		"ALTER INDEX idx_e2ee_keys_room_id RENAME TO idx_e2_ee_keys_room_id",
	} {
		if !strings.Contains(upText, want) {
			t.Errorf("%s 缺少 %q", upFile, want)
		}
	}
	for _, want := range []string{
		"RENAME CONSTRAINT minigame_leaderboards_game_type_user_id_key TO minigame_leaderboard_game_type_user_id_key",
		"ALTER INDEX idx_minigame_leaderboards_game_score RENAME TO idx_minigame_leaderboard_game_score",
		"ALTER INDEX idx_minigame_leaderboards_user RENAME TO idx_minigame_leaderboard_user",
		"ALTER INDEX idx_e2_ee_keys_room_id RENAME TO idx_e2ee_keys_room_id",
	} {
		if !strings.Contains(downText, want) {
			t.Errorf("%s 缺少 %q", downFile, want)
		}
	}
}
