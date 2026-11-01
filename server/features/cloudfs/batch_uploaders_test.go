package cloudfs

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"testing"
	"time"

	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/tests/testutil"
)

// countingLogger 包一层 gorm logger，只统计「查 users 表」的语句条数。
//
// 这组用例要证明的是「上传者展示名只查一次」而不是「结果看起来对」：
// 旧实现是循环里逐条 First(&u)，3 个文件就 3 条 SELECT ... FROM users；
// 新实现必须恰好 1 条 WHERE id IN (...)。
type countingLogger struct {
	inner gormlogger.Interface

	mu          sync.Mutex
	userQueries int
}

func (l *countingLogger) LogMode(level gormlogger.LogLevel) gormlogger.Interface {
	return &countingLogger{inner: l.inner.LogMode(level)}
}

func (l *countingLogger) Info(ctx context.Context, msg string, data ...interface{}) {
	l.inner.Info(ctx, msg, data...)
}

func (l *countingLogger) Warn(ctx context.Context, msg string, data ...interface{}) {
	l.inner.Warn(ctx, msg, data...)
}

func (l *countingLogger) Error(ctx context.Context, msg string, data ...interface{}) {
	l.inner.Error(ctx, msg, data...)
}

func (l *countingLogger) Trace(ctx context.Context, begin time.Time, fc func() (string, int64), err error) {
	sql, rows := fc()
	lower := strings.ToLower(sql)
	if strings.Contains(lower, "from users") || strings.Contains(lower, "from `users`") {
		l.mu.Lock()
		l.userQueries++
		l.mu.Unlock()
	}
	// 复用已取出的 SQL/rows，避免第二次调用 fc 触发重复渲染。
	l.inner.Trace(ctx, begin, func() (string, int64) { return sql, rows }, err)
}

func (l *countingLogger) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.userQueries
}

// setupBatchService 建库 + 空间 + 成员 + 两个用户，并返回挂了计数 logger 的 service。
func setupBatchService(t *testing.T) (*Service, *gorm.DB, string, *countingLogger) {
	t.Helper()
	db := testutil.MustSetupTestDB()
	space := &model.Space{ID: idgen.NextString(), Name: "batch-space", OwnerID: "user-a"}
	if err := db.Create(space).Error; err != nil {
		t.Fatalf("create space: %v", err)
	}
	if err := db.Create(&model.Membership{ID: idgen.NextString(), SpaceID: space.ID, UserID: "user-a", Role: "OWNER"}).Error; err != nil {
		t.Fatalf("create membership: %v", err)
	}
	for _, u := range []model.User{
		{ID: "user-a", Username: "alice", Email: "alice@example.com", DisplayName: "Alice"},
		{ID: "user-b", Username: "bob", Email: "bob@example.com", DisplayName: ""},
	} {
		if err := db.Create(&u).Error; err != nil {
			t.Fatalf("create user %s: %v", u.ID, err)
		}
	}

	counter := &countingLogger{inner: gormlogger.Discard}
	logged := db.Session(&gorm.Session{Logger: counter})
	svc := NewService(logged, &config.Config{LocalDataPath: t.TempDir()})
	return svc, db, space.ID, counter
}

func seedBatchFile(t *testing.T, db *gorm.DB, spaceID, folderID, name, uploadedBy string) {
	t.Helper()
	entry := &model.SharedFileEntry{
		ID:         idgen.GenerateID(idgen.PrefixFile),
		SpaceID:    spaceID,
		FolderID:   folderID,
		FileName:   name,
		FilePath:   "/" + name,
		FileSize:   1,
		UploadedBy: uploadedBy,
	}
	if err := db.Create(entry).Error; err != nil {
		t.Fatalf("create file %s: %v", name, err)
	}
}

// 3 个文件、2 个真实上传者 + 1 个已删除用户：只能有 1 条 users 查询，
// 展示名规则保持 displayName 优先、为空回退 username、用户不存在给空串。
func TestListBatchesUploaderLookup(t *testing.T) {
	svc, db, spaceID, counter := setupBatchService(t)
	seedBatchFile(t, db, spaceID, "", "a1.txt", "user-a")
	seedBatchFile(t, db, spaceID, "", "b1.txt", "user-b")
	seedBatchFile(t, db, spaceID, "", "ghost.txt", "user-ghost")

	items, _, err := svc.List(spaceID, "/", "user-a")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("expected 3 items, got %d", len(items))
	}
	byName := map[string]string{}
	for _, it := range items {
		byName[it.Name] = it.UploadBy
	}
	if byName["a1.txt"] != "Alice" {
		t.Errorf("a1.txt uploadBy = %q, want Alice", byName["a1.txt"])
	}
	if byName["b1.txt"] != "bob" {
		t.Errorf("b1.txt uploadBy = %q, want bob (username fallback)", byName["b1.txt"])
	}
	if byName["ghost.txt"] != "" {
		t.Errorf("ghost.txt uploadBy = %q, want empty for deleted user", byName["ghost.txt"])
	}
	if got := counter.count(); got != 1 {
		t.Errorf("uploader lookup must be batched: expected 1 users query, got %d", got)
	}
}

func TestSearchBatchesUploaderLookup(t *testing.T) {
	svc, db, spaceID, counter := setupBatchService(t)
	seedBatchFile(t, db, spaceID, "", "report-1.txt", "user-a")
	seedBatchFile(t, db, spaceID, "", "report-2.txt", "user-b")
	seedBatchFile(t, db, spaceID, "", "report-3.txt", "user-a")

	items, err := svc.Search(spaceID, "report", "", "user-a")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("expected 3 hits, got %d", len(items))
	}
	if got := counter.count(); got != 1 {
		t.Errorf("search uploader lookup must be batched: expected 1 users query, got %d", got)
	}
}

func TestTrashBatchesUploaderLookup(t *testing.T) {
	svc, db, spaceID, counter := setupBatchService(t)
	for _, name := range []string{"t1.txt", "t2.txt", "t3.txt"} {
		seedBatchFile(t, db, spaceID, "", name, "user-a")
	}
	if err := db.Where("space_id = ?", spaceID).Delete(&model.SharedFileEntry{}).Error; err != nil {
		t.Fatalf("soft delete files: %v", err)
	}

	items, err := svc.ListTrash(spaceID, "user-a")
	if err != nil {
		t.Fatalf("list trash: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("expected 3 trash items, got %d", len(items))
	}
	for _, it := range items {
		if it.UploadBy != "Alice" {
			t.Errorf("%s uploadBy = %q, want Alice", it.Name, it.UploadBy)
		}
	}
	if got := counter.count(); got != 1 {
		t.Errorf("trash uploader lookup must be batched: expected 1 users query, got %d", got)
	}
}

// 负向：批量查询失败必须上抛（旧实现会静默给空展示名）。
func TestListSurfacesUploaderLookupFailure(t *testing.T) {
	svc, db, spaceID, _ := setupBatchService(t)
	seedBatchFile(t, db, spaceID, "", "a1.txt", "user-a")

	if err := db.Migrator().DropTable(&model.User{}); err != nil {
		t.Fatalf("drop users table: %v", err)
	}
	items, _, err := svc.List(spaceID, "/", "user-a")
	if err == nil {
		t.Fatalf("expected error when uploader lookup fails, got items=%d", len(items))
	}
	if items != nil {
		t.Errorf("expected nil items on failure, got %d", len(items))
	}
}

// 可观测保护：超过阈值只告警、不截断（分页留到 1.9.6）。
func TestListWarnsButDoesNotTruncateOverThreshold(t *testing.T) {
	svc, db, spaceID, _ := setupBatchService(t)

	rows := make([]model.SharedFileEntry, 0, cloudFSListWarnThreshold+1)
	for i := 0; i < cloudFSListWarnThreshold+1; i++ {
		name := fmt.Sprintf("bulk-%05d.txt", i)
		rows = append(rows, model.SharedFileEntry{
			ID:         idgen.GenerateID(idgen.PrefixFile),
			SpaceID:    spaceID,
			FolderID:   "",
			FileName:   name,
			FilePath:   "/" + name,
			FileSize:   1,
			UploadedBy: "user-a",
		})
	}
	if err := db.CreateInBatches(rows, 500).Error; err != nil {
		t.Fatalf("seed bulk files: %v", err)
	}

	var buf bytes.Buffer
	orig := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(orig)

	items, _, err := svc.List(spaceID, "/", "user-a")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(items) != cloudFSListWarnThreshold+1 {
		t.Errorf("list must not truncate: got %d items, want %d", len(items), cloudFSListWarnThreshold+1)
	}
	if !strings.Contains(buf.String(), "WARN cloudfs list > 5000") {
		t.Errorf("expected over-threshold warning, log=%q", buf.String())
	}
}
