package cloudfs

import (
	"fmt"
	"testing"
	"time"

	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/tests/testutil"
)

// 1.9.5 task-138（规则 2）：回收站列表必须有上限，避免一次把整表返回。
//
// 口径（Lead 拍板）：只加 LIMIT，不加 page/cursor 参数、不改响应结构；
// 响应最多 200 条（超限静默截断），数据库侧最多取 1000 条。

func TestListTrashCapsResponseAtDefaultLimit(t *testing.T) {
	db := testutil.MustSetupTestDB()
	cfg := &config.Config{LocalDataPath: t.TempDir()}
	svc := NewService(db, cfg)

	space := &model.Space{ID: idgen.NextString(), Name: "trash-limit", OwnerID: "user-1"}
	if err := db.Create(space).Error; err != nil {
		t.Fatalf("建空间失败: %v", err)
	}
	if err := db.Create(&model.Membership{
		ID: idgen.NextString(), SpaceID: space.ID, UserID: "user-1", Role: "MEMBER",
	}).Error; err != nil {
		t.Fatalf("建成员关系失败: %v", err)
	}

	// 造 250 条已删除文件（超过默认上限 200），删除时间从新到旧。
	now := time.Now()
	for i := 0; i < 250; i++ {
		row := &model.SharedFileEntry{
			ID:         idgen.NextString(),
			SpaceID:    space.ID,
			FileName:   fmt.Sprintf("old-%03d.txt", i),
			FilePath:   fmt.Sprintf("/old-%03d.txt", i),
			FileSize:   10,
			UploadedBy: "user-1",
		}
		if err := db.Create(row).Error; err != nil {
			t.Fatalf("建文件行失败: %v", err)
		}
		if err := db.Unscoped().Model(&model.SharedFileEntry{}).
			Where("id = ?", row.ID).
			Update("deleted_at", now.Add(-time.Duration(i)*time.Minute)).Error; err != nil {
			t.Fatalf("软删除文件行失败: %v", err)
		}
	}

	items, err := svc.ListTrash(space.ID, "user-1")
	if err != nil {
		t.Fatalf("列回收站失败: %v", err)
	}
	if len(items) != trashListDefaultLimit {
		t.Fatalf("回收站列表应截断到 %d 条，实际 %d 条", trashListDefaultLimit, len(items))
	}
	if items[0].Name != "old-000.txt" {
		t.Fatalf("截断应保留「最近删除」的条目，首条期望 old-000.txt，实际 %q", items[0].Name)
	}
}
