package thirdparty

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// 这些用例覆盖 AGENTS.md 硬规则 4 的四个约束：超时、大小上限、有界重试
// （仅对可重试错误）、请求可取消。

func TestDownloadFileSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "hello-livekit")
	}))
	defer srv.Close()

	path, err := downloadFile(context.Background(), srv.URL, nil)
	if err != nil {
		t.Fatalf("downloadFile: %v", err)
	}
	defer os.Remove(path)

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read downloaded file: %v", err)
	}
	if string(got) != "hello-livekit" {
		t.Fatalf("unexpected body: %q", got)
	}
}

func TestDownloadFileRetriesTransient5xx(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&hits, 1)
		if n <= 2 {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		_, _ = io.WriteString(w, "ok-after-retry")
	}))
	defer srv.Close()

	var logs []string
	path, err := downloadFile(context.Background(), srv.URL, func(msg string, args ...any) {
		logs = append(logs, msg)
	})
	if err != nil {
		t.Fatalf("downloadFile should succeed after retries: %v", err)
	}
	defer os.Remove(path)

	if got := atomic.LoadInt32(&hits); got != 3 {
		t.Fatalf("expected 3 attempts (2 failures + 1 success), got %d", got)
	}
	if len(logs) != 2 {
		t.Fatalf("expected 2 retry log lines, got %d: %v", len(logs), logs)
	}
	if !strings.Contains(logs[0], "retrying in") {
		t.Fatalf("retry log should mention backoff, got %q", logs[0])
	}
}

func TestDownloadFileDoesNotRetry4xx(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		http.Error(w, "not found", http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := downloadFile(context.Background(), srv.URL, nil)
	if err == nil {
		t.Fatal("expected error for HTTP 404")
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("4xx must not be retried, got %d attempts", got)
	}
	var statusErr *httpStatusError
	if !errors.As(err, &statusErr) || statusErr.code != http.StatusNotFound {
		t.Fatalf("expected httpStatusError{404}, got %v", err)
	}
}

func TestDownloadFileHonorsContextDeadline(t *testing.T) {
	blocked := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-blocked:
		case <-r.Context().Done():
		}
	}))
	defer srv.Close()
	defer close(blocked)

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := downloadFile(ctx, srv.URL, nil)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
		t.Fatalf("expected context deadline/canceled, got %v", err)
	}
	// 必须早于服务端自身放行（否则说明没有超时约束）。
	if elapsed > 2*time.Second {
		t.Fatalf("download was not bounded by context: took %s", elapsed)
	}
}

func TestDownloadFileRejectsOversizeContentLength(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "1073741824") // 1 GiB > downloadMaxBytes
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	_, err := downloadFile(context.Background(), srv.URL, nil)
	if err == nil {
		t.Fatal("expected oversize error")
	}
	if !errors.Is(err, errDownloadTooLarge) {
		t.Fatalf("expected errDownloadTooLarge, got %v", err)
	}
}

func TestIsRetryableDownloadErr(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"too large", errDownloadTooLarge, false},
		{"canceled", context.Canceled, false},
		{"deadline", context.DeadlineExceeded, true},
		{"404", &httpStatusError{code: 404}, false},
		{"401", &httpStatusError{code: 401}, false},
		{"429", &httpStatusError{code: 429}, true},
		{"500", &httpStatusError{code: 500}, true},
		{"503", &httpStatusError{code: 503}, true},
		{"net error", &net.DNSError{Err: "no such host", IsTimeout: true}, true},
		{"plain error", errors.New("disk full"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isRetryableDownloadErr(tc.err); got != tc.want {
				t.Fatalf("isRetryableDownloadErr(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// 确认下载失败时不残留临时文件（成功路径由用例自己删除）。
func TestDownloadFileCleansTempOnFailure(t *testing.T) {
	before := countDownloadTempFiles(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 声明长度 8 字节却只写 3 字节：读取阶段 unexpected EOF（可重试），
		// 三次都失败后应返回错误并清理临时文件。
		w.Header().Set("Content-Length", "8")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "abc")
	}))
	defer srv.Close()

	if _, err := downloadFile(context.Background(), srv.URL, nil); err == nil {
		t.Fatal("expected truncated-download error")
	}
	after := countDownloadTempFiles(t)
	if after > before {
		t.Fatalf("temp files leaked: before=%d after=%d", before, after)
	}
}

func countDownloadTempFiles(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir(os.TempDir())
	if err != nil {
		t.Fatalf("read temp dir: %v", err)
	}
	n := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "ridgericetalk-download-") {
			n++
		}
	}
	return n
}
