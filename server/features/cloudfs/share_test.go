package cloudfs

import (
	"encoding/base64"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sqlite "github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"ridgericetalk/core/crypto"
	"ridgericetalk/core/errors"
	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/tests/testutil"
)

// ─────────────────────────────────────────────────────────────────────────────
// DES-2026-0912-05 分享链接：服务层安全测试
// ─────────────────────────────────────────────────────────────────────────────

// setupShareService 建一个隔离的 cloudfs Service（临时 LocalDataPath + 全新测试库）、
// 一个空间、一个成员与一个已落盘的文件。
func setupShareService(t *testing.T) (*Service, *gorm.DB, model.Space, *model.SharedFileEntry) {
	t.Helper()
	db := testutil.MustSetupTestDB()
	dataPath := t.TempDir()
	svc := NewService(db, &config.Config{LocalDataPath: dataPath, PublicAddress: "https://rt.example"})

	space := model.Space{ID: idgen.NextString(), Name: "share-space", OwnerID: "user-1"}
	if err := db.Create(&space).Error; err != nil {
		t.Fatalf("create space: %v", err)
	}
	if err := db.Create(&model.Membership{ID: idgen.NextString(), SpaceID: space.ID, UserID: "user-1", Role: "MEMBER"}).Error; err != nil {
		t.Fatalf("create membership: %v", err)
	}
	if err := db.Create(&model.Membership{ID: idgen.NextString(), SpaceID: space.ID, UserID: "admin-1", Role: "ADMIN"}).Error; err != nil {
		t.Fatalf("create admin membership: %v", err)
	}

	entry := seedStoredFile(t, db, filepath.Join(dataPath, "cloudfs"), space.ID, "",
		"report.pdf", "/report.pdf", "application/pdf", "pdf-bytes")
	return svc, db, space, entry
}

// assertErrCode 断言错误是带指定错误码的 AppError。
func assertErrCode(t *testing.T, err error, want errors.ErrorCode) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error %s, got nil", want)
	}
	appErr, ok := err.(*errors.AppError)
	if !ok {
		t.Fatalf("expected *AppError with code %s, got %T: %v", want, err, err)
	}
	if appErr.Code != want {
		t.Fatalf("error code = %s, want %s", appErr.Code, want)
	}
}

// rawShareRow 以字符串形式返回某一行的所有列，用于「库里没有明文」这类断言。
func rawShareRow(t *testing.T, db *gorm.DB, shareID string) map[string]string {
	t.Helper()
	rows, err := db.Raw("SELECT * FROM cloud_file_shares WHERE id = ?", shareID).Rows()
	if err != nil {
		t.Fatalf("raw select: %v", err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatalf("columns: %v", err)
	}
	if !rows.Next() {
		t.Fatalf("share row %s not found", shareID)
	}
	vals := make([]interface{}, len(cols))
	ptrs := make([]interface{}, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		t.Fatalf("scan: %v", err)
	}
	out := make(map[string]string, len(cols))
	for i, col := range cols {
		switch v := vals[i].(type) {
		case nil:
			out[col] = ""
		case []byte:
			out[col] = string(v)
		case string:
			out[col] = v
		default:
			out[col] = fmt.Sprintf("%v", v)
		}
	}
	return out
}

// TestGenerateShareTokenIsRandomUniqueAndUrlSafe 覆盖设计 §4 的令牌生成要求。
func TestGenerateShareTokenIsRandomUniqueAndUrlSafe(t *testing.T) {
	const n = 300
	seenTokens := make(map[string]bool, n)
	seenHashes := make(map[string]bool, n)

	for i := 0; i < n; i++ {
		token, hash, prefix, err := GenerateShareToken()
		if err != nil {
			t.Fatalf("GenerateShareToken: %v", err)
		}
		raw, err := base64.RawURLEncoding.DecodeString(token)
		if err != nil {
			t.Fatalf("token %q is not RawURLEncoding base64: %v", token, err)
		}
		if len(raw) != 32 {
			t.Fatalf("token entropy = %d bytes, want 32 bytes (256 bit)", len(raw))
		}
		if len(hash) != 64 {
			t.Fatalf("hash = %q, want 64 hex chars", hash)
		}
		if hash == token {
			t.Fatal("hash must never equal the token")
		}
		if got := HashShareToken(token); got != hash {
			t.Fatalf("HashShareToken mismatch: %s != %s", got, hash)
		}
		if prefix != token[:8] {
			t.Fatalf("prefix = %q, want first 8 chars of token", prefix)
		}
		if strings.ContainsAny(token, "+/=") {
			t.Fatalf("token %q contains characters that are not URL-safe", token)
		}
		if seenTokens[token] {
			t.Fatalf("duplicate token generated: %s", token)
		}
		if seenHashes[hash] {
			t.Fatalf("duplicate token hash generated: %s", hash)
		}
		seenTokens[token] = true
		seenHashes[hash] = true
	}
}

// TestCreateSharePersistsOnlyHashes 证明库里既没有明文 token 也没有明文密码（设计 §2/§4）。
func TestCreateSharePersistsOnlyHashes(t *testing.T) {
	svc, db, space, entry := setupShareService(t)

	const plainPassword = "s3cret-password"
	share, token, err := svc.CreateShare(space.ID, "user-1", ShareOptions{
		FileID:   entry.ID,
		Password: plainPassword,
	})
	if err != nil {
		t.Fatalf("CreateShare: %v", err)
	}
	if share.TokenHash != HashShareToken(token) {
		t.Fatalf("stored token hash = %s, want sha256(token)", share.TokenHash)
	}
	if share.TokenPrefix != token[:8] {
		t.Fatalf("token prefix = %q, want %q", share.TokenPrefix, token[:8])
	}
	if !strings.HasPrefix(share.ID, "share_") {
		t.Fatalf("share ID = %q, want share_<id>", share.ID)
	}
	if share.PasswordHash == plainPassword || share.PasswordHash == "" {
		t.Fatal("password must be stored as a bcrypt hash")
	}
	if !crypto.CheckPassword(plainPassword, share.PasswordHash) {
		t.Fatal("stored password hash does not verify against the plaintext")
	}
	// cost 与项目既有口令存储一致（默认 12）。
	if want := fmt.Sprintf("$2a$%02d$", crypto.DefaultBcryptCost); !strings.HasPrefix(share.PasswordHash, want) {
		t.Fatalf("bcrypt cost mismatch: hash %q does not start with %q", share.PasswordHash, want)
	}
	if share.ExpiresAt.Before(time.Now().UTC().Add(6 * 24 * time.Hour)) {
		t.Fatalf("default TTL = %v, want ~7 days", time.Until(share.ExpiresAt))
	}

	// 逐列扫描：任何一列都不允许出现明文 token / 明文密码。
	for col, value := range rawShareRow(t, db, share.ID) {
		if strings.Contains(value, token) {
			t.Fatalf("plaintext token leaked into column %q", col)
		}
		if strings.Contains(value, plainPassword) {
			t.Fatalf("plaintext password leaked into column %q", col)
		}
	}

	// 明文 token 也不能出现在 token_hash 列里（即「拿明文当哈希存」）。
	var count int64
	if err := db.Raw("SELECT COUNT(*) FROM cloud_file_shares WHERE token_hash = ?", token).Scan(&count).Error; err != nil {
		t.Fatalf("count query: %v", err)
	}
	if count != 0 {
		t.Fatalf("plaintext token found in token_hash column (%d rows)", count)
	}
}

// TestCreateShareValidatesTTLAndOptions 覆盖设计 §6/§11-1（默认 7 天、上限 30 天）。
func TestCreateShareValidatesTTLAndOptions(t *testing.T) {
	svc, db, space, entry := setupShareService(t)

	cases := []struct {
		name    string
		opts    ShareOptions
		wantErr errors.ErrorCode
	}{
		{"default ttl", ShareOptions{FileID: entry.ID}, ""},
		{"max ttl", ShareOptions{FileID: entry.ID, ExpiresInDays: ShareMaxTTLDays}, ""},
		{"ttl above cap", ShareOptions{FileID: entry.ID, ExpiresInDays: ShareMaxTTLDays + 1}, errors.SHARE_TTL_INVALID},
		{"negative ttl", ShareOptions{FileID: entry.ID, ExpiresInDays: -1}, errors.SHARE_TTL_INVALID},
		{"negative max downloads", ShareOptions{FileID: entry.ID, MaxDownloads: -3}, errors.SHARE_MAX_DOWNLOADS_INVALID},
		{"password too long", ShareOptions{FileID: entry.ID, Password: strings.Repeat("x", ShareMaxPasswordLen+1)}, errors.SHARE_PASSWORD_INVALID},
		{"missing file", ShareOptions{FileID: ""}, errors.SYSTEM_BAD_REQUEST},
		{"unknown file", ShareOptions{FileID: "file_does_not_exist"}, errors.CLOUDFS_FILE_NOT_FOUND},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			share, _, err := svc.CreateShare(space.ID, "user-1", tc.opts)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("CreateShare: %v", err)
				}
				return
			}
			assertErrCode(t, err, tc.wantErr)
			if share != nil {
				t.Fatal("no share row may be created when validation fails")
			}
		})
	}

	var total int64
	if err := db.Model(&model.CloudFileShare{}).Count(&total).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if total != 2 {
		t.Fatalf("created %d shares, want only the 2 valid ones", total)
	}
}

// TestCreateShareRequiresSpaceMembership 覆盖设计 §5/§10「越权分享不可见文件」。
func TestCreateShareRequiresSpaceMembership(t *testing.T) {
	svc, db, space, entry := setupShareService(t)

	// 1) 完全不是该空间成员的用户。
	_, _, err := svc.CreateShare(space.ID, "outsider", ShareOptions{FileID: entry.ID})
	assertErrCode(t, err, errors.CLOUDFS_PERMISSION_DENIED)

	// 2) 是别的空间的成员 —— 仍不能分享本空间的文件。
	otherSpace := model.Space{ID: idgen.NextString(), Name: "other-space", OwnerID: "user-2"}
	if err := db.Create(&otherSpace).Error; err != nil {
		t.Fatalf("create other space: %v", err)
	}
	if err := db.Create(&model.Membership{ID: idgen.NextString(), SpaceID: otherSpace.ID, UserID: "user-2", Role: "MEMBER"}).Error; err != nil {
		t.Fatalf("create other membership: %v", err)
	}
	_, _, err = svc.CreateShare(otherSpace.ID, "user-2", ShareOptions{FileID: entry.ID})
	assertErrCode(t, err, errors.CLOUDFS_FILE_NOT_FOUND)

	// 3) 空间成员可以分享（设计 §11-3：文件可见范围内的成员均可）。
	if _, _, err := svc.CreateShare(space.ID, "user-1", ShareOptions{FileID: entry.ID}); err != nil {
		t.Fatalf("space member must be allowed to create a share: %v", err)
	}

	// 4) 已删除（进回收站）的文件不能被分享。
	if err := db.Delete(&model.SharedFileEntry{}, "id = ?", entry.ID).Error; err != nil {
		t.Fatalf("soft delete entry: %v", err)
	}
	_, _, err = svc.CreateShare(space.ID, "user-1", ShareOptions{FileID: entry.ID})
	assertErrCode(t, err, errors.CLOUDFS_FILE_NOT_FOUND)
}

// TestClaimShareDownloadRejectsExpiredRevokedAndExhausted 覆盖设计 §6 的三类拒绝。
func TestClaimShareDownloadRejectsExpiredRevokedAndExhausted(t *testing.T) {
	svc, db, space, entry := setupShareService(t)

	t.Run("expired", func(t *testing.T) {
		share, _, err := svc.CreateShare(space.ID, "user-1", ShareOptions{FileID: entry.ID})
		if err != nil {
			t.Fatalf("CreateShare: %v", err)
		}
		// 模拟链接自然到期。
		if err := db.Model(&model.CloudFileShare{}).Where("id = ?", share.ID).
			Update("expires_at", time.Now().UTC().Add(-time.Minute)).Error; err != nil {
			t.Fatalf("expire share: %v", err)
		}
		_, err = svc.ClaimShareDownload(share.ID)
		assertErrCode(t, err, errors.SHARE_EXPIRED)

		var after model.CloudFileShare
		if err := db.First(&after, "id = ?", share.ID).Error; err != nil {
			t.Fatalf("reload: %v", err)
		}
		if after.DownloadCount != 0 {
			t.Fatalf("download_count = %d, want 0 after a rejected claim", after.DownloadCount)
		}
	})

	t.Run("revoked", func(t *testing.T) {
		share, _, err := svc.CreateShare(space.ID, "user-1", ShareOptions{FileID: entry.ID})
		if err != nil {
			t.Fatalf("CreateShare: %v", err)
		}
		if _, err := svc.RevokeShare(space.ID, "user-1", share.ID); err != nil {
			t.Fatalf("RevokeShare: %v", err)
		}
		_, err = svc.ClaimShareDownload(share.ID)
		assertErrCode(t, err, errors.SHARE_REVOKED)

		// 撤销是永久的：再撤销一次不会清空 revoked_at。
		again, err := svc.RevokeShare(space.ID, "user-1", share.ID)
		if err != nil {
			t.Fatalf("second revoke: %v", err)
		}
		if again.RevokedAt == nil {
			t.Fatal("revoked_at must never be cleared")
		}
		_, err = svc.ClaimShareDownload(share.ID)
		assertErrCode(t, err, errors.SHARE_REVOKED)
	})

	t.Run("download limit", func(t *testing.T) {
		share, _, err := svc.CreateShare(space.ID, "user-1", ShareOptions{FileID: entry.ID, MaxDownloads: 2})
		if err != nil {
			t.Fatalf("CreateShare: %v", err)
		}
		for i := 1; i <= 2; i++ {
			count, err := svc.ClaimShareDownload(share.ID)
			if err != nil {
				t.Fatalf("claim %d: %v", i, err)
			}
			if count != i {
				t.Fatalf("download_count after claim %d = %d, want %d", i, count, i)
			}
		}
		_, err = svc.ClaimShareDownload(share.ID)
		assertErrCode(t, err, errors.SHARE_DOWNLOAD_LIMITED)

		var after model.CloudFileShare
		if err := db.First(&after, "id = ?", share.ID).Error; err != nil {
			t.Fatalf("reload: %v", err)
		}
		if after.DownloadCount != 2 {
			t.Fatalf("download_count = %d, want it capped at 2", after.DownloadCount)
		}
		if after.LastAccessAt == nil {
			t.Fatal("last_access_at must be stamped on a successful claim")
		}
	})

	t.Run("zero means unlimited", func(t *testing.T) {
		share, _, err := svc.CreateShare(space.ID, "user-1", ShareOptions{FileID: entry.ID, MaxDownloads: 0})
		if err != nil {
			t.Fatalf("CreateShare: %v", err)
		}
		for i := 0; i < 7; i++ {
			if _, err := svc.ClaimShareDownload(share.ID); err != nil {
				t.Fatalf("claim %d: %v", i+1, err)
			}
		}
	})
}

// TestConcurrentShareDownloadNeverOversells 是设计 §6 的核心并发用例：
// 先原子占用、再返回文件，成功次数必须**恰好**等于 max_downloads。
//
// 这里刻意用文件型 SQLite + 多个连接（testutil 的内存库只有 1 个连接，压不出并发），
// 并用 40 个 goroutine 争抢 5 个名额。
func TestConcurrentShareDownloadNeverOversells(t *testing.T) {
	const (
		maxDownloads = 5
		contenders   = 40
	)
	db := openConcurrentShareDB(t)
	svc := NewService(db, &config.Config{LocalDataPath: t.TempDir()})

	share := &model.CloudFileShare{
		ID:           idgen.GenerateID(idgen.PrefixShare),
		FileID:       "file_concurrent",
		SpaceID:      "space_concurrent",
		OwnerID:      "user-1",
		TokenHash:    HashShareToken("concurrent-token"),
		TokenPrefix:  "concurre",
		ExpiresAt:    time.Now().UTC().Add(time.Hour),
		MaxDownloads: maxDownloads,
		CreatedAt:    time.Now().UTC(),
	}
	if err := db.Create(share).Error; err != nil {
		t.Fatalf("create share: %v", err)
	}

	var successes, limited, unexpected int64
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			// SQLite 单写者：偶发写锁错误时重试，只让「成功 / 达上限」成为终态。
			for attempt := 0; attempt < 5; attempt++ {
				_, err := svc.ClaimShareDownload(share.ID)
				if err == nil {
					atomic.AddInt64(&successes, 1)
					return
				}
				if appErr, ok := err.(*errors.AppError); ok && appErr.Code == errors.SHARE_DOWNLOAD_LIMITED {
					atomic.AddInt64(&limited, 1)
					return
				}
				time.Sleep(5 * time.Millisecond)
			}
			atomic.AddInt64(&unexpected, 1)
		}()
	}
	close(start)
	wg.Wait()

	if unexpected != 0 {
		t.Fatalf("%d claims ended with an unexpected error instead of success/limit", unexpected)
	}
	if successes != maxDownloads {
		t.Fatalf("concurrent claims succeeded %d times, want exactly maxDownloads=%d (oversell/undersell)", successes, maxDownloads)
	}
	if limited != contenders-maxDownloads {
		t.Fatalf("rejected claims = %d, want %d", limited, contenders-maxDownloads)
	}

	var after model.CloudFileShare
	if err := db.First(&after, "id = ?", share.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if after.DownloadCount != maxDownloads {
		t.Fatalf("final download_count = %d, want %d", after.DownloadCount, maxDownloads)
	}
}

// openConcurrentShareDB 打开一个可并发的（文件型、多连接）SQLite 测试库。
// 只迁移本用例需要的表，因此没有直接复用 testutil.SetupTestDB（后者固定单连接）。
func openConcurrentShareDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := filepath.Join(t.TempDir(), "share_concurrency.db") +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("open concurrent db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("sql.DB: %v", err)
	}
	sqlDB.SetMaxOpenConns(8)
	if err := db.AutoMigrate(&model.CloudFileShare{}); err != nil {
		t.Fatalf("migrate concurrent db: %v", err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db
}

// TestGetShareByTokenRejectsUnknownTamperedAndPrefixOnly 覆盖令牌不可枚举的读取侧。
func TestGetShareByTokenRejectsUnknownTamperedAndPrefixOnly(t *testing.T) {
	svc, _, space, entry := setupShareService(t)

	share, token, err := svc.CreateShare(space.ID, "user-1", ShareOptions{FileID: entry.ID})
	if err != nil {
		t.Fatalf("CreateShare: %v", err)
	}

	found, err := svc.GetShareByToken(token)
	if err != nil {
		t.Fatalf("GetShareByToken(valid): %v", err)
	}
	if found.ID != share.ID {
		t.Fatalf("resolved share = %s, want %s", found.ID, share.ID)
	}

	tampered := []byte(token)
	if tampered[0] == 'A' {
		tampered[0] = 'B'
	} else {
		tampered[0] = 'A'
	}

	for name, candidate := range map[string]string{
		"empty":       "",
		"unknown":     "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
		"tampered":    string(tampered),
		"prefix only": share.TokenPrefix,
		"uppercased":  strings.ToUpper(token),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := svc.GetShareByToken(candidate); err == nil {
				t.Fatalf("token %q must not resolve to a share", candidate)
			}
		})
	}
}

// TestShareTicketStoreIsSingleUseAndBoundToShare 覆盖设计 §5 的一次性下载凭据。
func TestShareTicketStoreIsSingleUseAndBoundToShare(t *testing.T) {
	store := newShareTicketStore()

	ticket, err := store.Issue("share_1")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if ticket == "" {
		t.Fatal("issued ticket must not be empty")
	}
	if !store.Consume("share_1", ticket) {
		t.Fatal("a freshly issued ticket must be accepted")
	}
	if store.Consume("share_1", ticket) {
		t.Fatal("a ticket must be single-use")
	}

	// 绑定关系：别的分享不能用自己的凭据解锁。
	second, err := store.Issue("share_1")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if store.Consume("share_2", second) {
		t.Fatal("a ticket must be bound to the share it was issued for")
	}
	if store.Consume("share_1", second) {
		t.Fatal("a failed binding check must still burn the ticket")
	}

	if store.Consume("share_1", "") {
		t.Fatal("an empty ticket must be rejected")
	}
	if store.Consume("share_1", "not-a-real-ticket") {
		t.Fatal("an unknown ticket must be rejected")
	}

	// 过期凭据。
	expired, err := store.Issue("share_1")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	store.mu.Lock()
	entry := store.tickets[HashShareToken(expired)]
	entry.expiresAt = time.Now().Add(-time.Second)
	store.tickets[HashShareToken(expired)] = entry
	store.mu.Unlock()
	if store.Consume("share_1", expired) {
		t.Fatal("an expired ticket must be rejected")
	}
}

// TestRevokeSharePermissionMatrix 撤销权限：创建者本人或空间管理员。
func TestRevokeSharePermissionMatrix(t *testing.T) {
	svc, db, space, entry := setupShareService(t)

	// 第三个普通成员（非创建者）。
	if err := db.Create(&model.Membership{ID: idgen.NextString(), SpaceID: space.ID, UserID: "user-9", Role: "MEMBER"}).Error; err != nil {
		t.Fatalf("create membership: %v", err)
	}

	share, _, err := svc.CreateShare(space.ID, "user-1", ShareOptions{FileID: entry.ID})
	if err != nil {
		t.Fatalf("CreateShare: %v", err)
	}

	// 非创建者的普通成员不能撤销。
	if _, err := svc.RevokeShare(space.ID, "user-9", share.ID); err == nil {
		t.Fatal("a non-owner member must not be able to revoke someone else's share")
	}
	// 非成员不能撤销。
	if _, err := svc.RevokeShare(space.ID, "outsider", share.ID); err == nil {
		t.Fatal("a non-member must not be able to revoke a share")
	}
	// 不存在的分享 → SHARE_NOT_FOUND。
	if _, err := svc.RevokeShare(space.ID, "user-1", "share_nope"); err == nil {
		t.Fatal("revoking an unknown share must fail")
	} else {
		assertErrCode(t, err, errors.SHARE_NOT_FOUND)
	}

	// 空间管理员可以撤销（管理端能力，设计 §9）。
	revoked, err := svc.RevokeShare(space.ID, "admin-1", share.ID)
	if err != nil {
		t.Fatalf("admin revoke: %v", err)
	}
	if revoked.RevokedAt == nil {
		t.Fatal("revoked_at must be set")
	}
}

// TestEvaluateShareReportsState 校验状态判定（元信息接口与 UI 依赖它）。
func TestEvaluateShareReportsState(t *testing.T) {
	svc, db, space, entry := setupShareService(t)
	share, _, err := svc.CreateShare(space.ID, "user-1", ShareOptions{FileID: entry.ID, MaxDownloads: 1})
	if err != nil {
		t.Fatalf("CreateShare: %v", err)
	}

	if state := svc.EvaluateShare(share); !state.Available {
		t.Fatalf("fresh share must be available: %+v", state)
	}
	if _, err := svc.ClaimShareDownload(share.ID); err != nil {
		t.Fatalf("claim: %v", err)
	}
	var reloaded model.CloudFileShare
	if err := db.First(&reloaded, "id = ?", share.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if state := svc.EvaluateShare(&reloaded); !state.Exhausted || state.Available {
		t.Fatalf("share at its limit must be exhausted: %+v", state)
	}
	if state := svc.EvaluateShare(&model.CloudFileShare{ExpiresAt: time.Now().UTC().Add(-time.Hour)}); !state.Expired {
		t.Fatal("past expiry must be reported as expired")
	}
	now := time.Now().UTC()
	if state := svc.EvaluateShare(&model.CloudFileShare{ExpiresAt: now.Add(time.Hour), RevokedAt: &now}); !state.Revoked {
		t.Fatal("revoked_at must be reported as revoked")
	}
}

// TestShareAuditLogIsWritten 覆盖设计 §8 的审计埋点。
func TestShareAuditLogIsWritten(t *testing.T) {
	svc, db, space, entry := setupShareService(t)

	share, _, err := svc.CreateShare(space.ID, "user-1", ShareOptions{FileID: entry.ID})
	if err != nil {
		t.Fatalf("CreateShare: %v", err)
	}
	svc.LogShareSecurityEvent("user-1", ShareAuditCreated, share.ID, "203.0.113.42", "test-agent",
		map[string]interface{}{"fileId": share.FileID, "hasPassword": false})
	svc.LogShareSecurityEvent("", ShareAuditDownloadDenied, share.ID, "203.0.113.42", "test-agent",
		map[string]interface{}{"reason": "expired"})

	var logs []model.SecurityAuditLog
	if err := db.Where("resource_id = ?", share.ID).Order("created_at asc").Find(&logs).Error; err != nil {
		t.Fatalf("query audit logs: %v", err)
	}
	if len(logs) != 2 {
		t.Fatalf("audit rows = %d, want 2", len(logs))
	}
	if logs[0].Action != ShareAuditCreated || logs[1].Action != ShareAuditDownloadDenied {
		t.Fatalf("unexpected audit actions: %s / %s", logs[0].Action, logs[1].Action)
	}
	if logs[0].ResourceType != "cloud_file_share" {
		t.Fatalf("resource type = %q", logs[0].ResourceType)
	}
	// 审计里不允许出现明文 token，IP 必须已做掩码。
	for _, l := range logs {
		if strings.Contains(l.DetailsJSON, "token") {
			t.Fatalf("audit details must not carry token material: %s", l.DetailsJSON)
		}
		if l.IPMasked != "203.0.113.0" {
			t.Fatalf("IP must be masked, got %q", l.IPMasked)
		}
	}
}

// TestShouldAuditDownloadSamples 采样策略：第 1 次必记，之后每 10 次记一次。
func TestShouldAuditDownloadSamples(t *testing.T) {
	if !ShouldAuditDownload(1) {
		t.Fatal("the first download must always be audited")
	}
	for _, n := range []int{2, 3, 9, 11} {
		if ShouldAuditDownload(n) {
			t.Fatalf("download #%d should be sampled out", n)
		}
	}
	for _, n := range []int{10, 20, 30} {
		if !ShouldAuditDownload(n) {
			t.Fatalf("download #%d should be audited", n)
		}
	}
}

// TestShareFileNamesResolvesOnlyLiveFiles 列表展示用文件名解析。
func TestShareFileNamesResolvesOnlyLiveFiles(t *testing.T) {
	svc, db, space, entry := setupShareService(t)
	share, _, err := svc.CreateShare(space.ID, "user-1", ShareOptions{FileID: entry.ID})
	if err != nil {
		t.Fatalf("CreateShare: %v", err)
	}
	shr := &model.CloudFileShare{ID: "share_deleted", FileID: "file_gone", SpaceID: space.ID}

	names := svc.ShareFileNames([]model.CloudFileShare{*share, *shr})
	if names[entry.ID] != "report.pdf" {
		t.Fatalf("resolved name = %q, want report.pdf", names[entry.ID])
	}
	if _, ok := names["file_gone"]; ok {
		t.Fatal("deleted files must not be resolvable")
	}

	// 已删除的文件从列表解析里消失。
	if err := db.Delete(&model.SharedFileEntry{}, "id = ?", entry.ID).Error; err != nil {
		t.Fatalf("soft delete: %v", err)
	}
	if names := svc.ShareFileNames([]model.CloudFileShare{*share}); len(names) != 0 {
		t.Fatalf("soft-deleted file must not resolve, got %v", names)
	}
}

// TestShareDownloadInfoGuardsPathTraversal 分享下载路径解析不得逃出 cloudfs 根目录。
func TestShareDownloadInfoGuardsPathTraversal(t *testing.T) {
	svc, _, _, _ := setupShareService(t)

	// 正常文件。
	entry := &model.SharedFileEntry{ID: "file_ok", PhysicalName: "abc_report.pdf", FileName: "report.pdf", MimeType: "application/pdf"}
	info, err := svc.shareDownloadInfo(entry)
	if err != nil {
		t.Fatalf("shareDownloadInfo: %v", err)
	}
	want := filepath.Join(svc.baseDir(), "abc_report.pdf")
	if info.PhysicalPath != want {
		t.Fatalf("physical path = %q, want %q", info.PhysicalPath, want)
	}

	// 目录穿越：PhysicalName 若被污染，必须被拒绝而不是照单执行。
	evil := &model.SharedFileEntry{ID: "file_evil", PhysicalName: filepath.Join("..", "..", "etc", "passwd"), FileName: "passwd"}
	if _, err := svc.shareDownloadInfo(evil); err == nil {
		t.Fatal("path traversal in physical_name must be rejected")
	}

	// 没有 physical_name 的老记录：直接按文件不存在处理（不做目录扫描回退）。
	if _, err := svc.shareDownloadInfo(&model.SharedFileEntry{ID: "file_old", FileName: "old.txt"}); err == nil {
		t.Fatal("entries without a physical name must not be served")
	}
}
