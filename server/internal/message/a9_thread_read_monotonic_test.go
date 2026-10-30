package message

// task-33：A9 二期 MarkThreadRead 并发乱序「只进不退」回归守卫（tester 独立用例）。
//
// 背景：DES-20261029-01 §7.2 要求线程已读位单调推进（read_at 只进不退），
// 且水位未推进的重复上报不广播归零事件。实现侧已有单点用例，本文件补两个
// 运行时视角的守卫：
//  1) 并发乱序上报（较新/较早 lastMessageId 同时打进来）后，水位必须停在最新回复，
//     不得因后到的旧上报回退；
//  2) 回退上报走 HTTP 时必须是 200 + data.message="thread already read"，
//     且不产生新的归零广播（BroadcastToUser 调用数不变）。
//
// 说明：包内复用 thread_unread_test.go 的 helper（newThreadMsg / storedThreadRead /
// assertApproxTime / newThreadRouter / doJSON），不重复造脚手架。

import (
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"ridgericetalk/tests/testutil"
)

// TestA9ThreadReadMonotonicConcurrentOutOfOrder 并发乱序上报：
// 8 个 goroutine 同时上报最新回复与较早回复，最终水位必须=最新回复，线程未读必须为 0。
func TestA9ThreadReadMonotonicConcurrentOutOfOrder(t *testing.T) {
	db := testutil.MustSetupTestDB()
	svc := NewService(db)

	parent := newThreadMsg(t, svc, "ch-a9-mon", "user-2", "parent", "")
	reply1 := newThreadMsg(t, svc, "ch-a9-mon", "user-2", "reply-1", parent.ID)
	time.Sleep(2 * time.Millisecond)
	reply2 := newThreadMsg(t, svc, "ch-a9-mon", "user-3", "reply-2", parent.ID)
	time.Sleep(2 * time.Millisecond)
	reply3 := newThreadMsg(t, svc, "ch-a9-mon", "user-2", "reply-3", parent.ID)

	order := []string{reply3.ID, reply1.ID, reply2.ID, reply3.ID, reply1.ID, reply3.ID, reply2.ID, reply1.ID}
	var wg sync.WaitGroup
	errCh := make(chan error, len(order))
	for _, lastID := range order {
		lastID := lastID
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := svc.MarkThreadRead("user-1", "ch-a9-mon", parent.ID, lastID); err != nil {
				errCh <- err
			}
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatalf("concurrent MarkThreadRead: %v", err)
	}

	row := storedThreadRead(t, db, "user-1", parent.ID)
	if row.LastReadMessageID != reply3.ID {
		t.Errorf("stored last_read_message_id = %s, want %s（乱序旧上报不得回退）", row.LastReadMessageID, reply3.ID)
	}
	assertApproxTime(t, row.ReadAt, reply3.CreatedAt, "并发乱序后 read_at 必须停在最新回复")

	info, err := svc.GetUnreadCount("user-1", "ch-a9-mon")
	if err != nil {
		t.Fatalf("GetUnreadCount: %v", err)
	}
	if tu, ok := findThreadUnread(info, parent.ID); ok {
		t.Errorf("thread unread after all replies read = %+v, want none", tu)
	}
}

// TestA9ThreadReadMonotonicRegressNoExtraResetBroadcast 回退上报：
// 先上报最新回复（应广播一次归零），再上报更早回复：必须 200 + "thread already read"，
// 且不再产生归零广播（hub 的 BroadcastToUser 调用数保持 1），已读位不回退。
func TestA9ThreadReadMonotonicRegressNoExtraResetBroadcast(t *testing.T) {
	h, r, rec := newThreadRouter(t)
	parent := newThreadMsg(t, h.service, "ch-a9-mon2", "user-2", "parent", "")
	reply1 := newThreadMsg(t, h.service, "ch-a9-mon2", "user-2", "reply-1", parent.ID)
	time.Sleep(2 * time.Millisecond)
	reply2 := newThreadMsg(t, h.service, "ch-a9-mon2", "user-3", "reply-2", parent.ID)

	post := func(lastMessageID string) (int, []byte) {
		body, _ := json.Marshal(map[string]string{"threadParentId": parent.ID, "lastMessageId": lastMessageID})
		w := doJSON(r, http.MethodPost, "/channels/ch-a9-mon2/mark-read", body)
		return w.Code, w.Body.Bytes()
	}

	if code, body := post(reply2.ID); code != http.StatusOK {
		t.Fatalf("first report status = %d, body=%s", code, body)
	}
	if got := len(rec.userCalls); got != 1 {
		t.Fatalf("first (advanced) report BroadcastToUser calls = %d, want 1", got)
	}

	code, body := post(reply1.ID)
	if code != http.StatusOK {
		t.Fatalf("regressive report status = %d, body=%s", code, body)
	}
	var resp struct {
		Code string                 `json:"code"`
		Data map[string]interface{} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode regressive response: %v", err)
	}
	if resp.Code != "OK" {
		t.Errorf("regressive report code = %q, want OK", resp.Code)
	}
	if resp.Data["message"] != "thread already read" {
		t.Errorf("regressive report data.message = %v, want \"thread already read\"", resp.Data["message"])
	}
	if got := len(rec.userCalls); got != 1 {
		t.Errorf("regressive report must not broadcast, BroadcastToUser calls = %d, want 1", got)
	}

	row := storedThreadRead(t, h.service.db, "user-1", parent.ID)
	if row.LastReadMessageID != reply2.ID {
		t.Errorf("stored last_read_message_id = %s, want %s（回退上报不得覆盖）", row.LastReadMessageID, reply2.ID)
	}
}
