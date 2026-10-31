package storage

import "testing"

func TestAbsoluteFilesBase(t *testing.T) {
	cases := []struct {
		name    string
		address string
		want    string
		ok      bool
	}{
		{"空地址", "", "", false},
		{"相对地址", "t46.sjcmc.cn", "", false},
		{"无端口（反代/缺配置场景）", "http://t46.sjcmc.cn", "", false},
		{"无端口 https", "https://talk.example.com", "", false},
		{"带端口", "http://t46.sjcmc.cn:21103", "http://t46.sjcmc.cn:21103/api/v1/files", true},
		{"带端口 + 末尾斜杠", "http://t46.sjcmc.cn:21103/", "http://t46.sjcmc.cn:21103/api/v1/files", true},
		{"带端口 + 子路径", "https://talk.example.com:8443/app", "https://talk.example.com:8443/api/v1/files", true},
		{"带端口 + 前后空格", "  http://192.0.2.10:8080  ", "http://192.0.2.10:8080/api/v1/files", true},
		{"默认端口显式写出", "https://talk.example.com:443", "https://talk.example.com:443/api/v1/files", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := AbsoluteFilesBase(tc.address)
			if ok != tc.ok {
				t.Fatalf("ok=%v，期望 %v（address=%q）", ok, tc.ok, tc.address)
			}
			if got != tc.want {
				t.Fatalf("base=%q，期望 %q", got, tc.want)
			}
		})
	}
}

func TestURLWithBase(t *testing.T) {
	key := "uploads/8c/90c6ae.txt"
	cases := []struct {
		name string
		base string
		want string
	}{
		{"空 base → 相对路径", "", "/api/v1/files/" + key},
		{"空白 base → 相对路径", "   ", "/api/v1/files/" + key},
		{"带端口 base", "http://t46.sjcmc.cn:21103/api/v1/files", "http://t46.sjcmc.cn:21103/api/v1/files/" + key},
		{"base 末尾斜杠不产生双斜杠", "http://h:8080/api/v1/files/", "http://h:8080/api/v1/files/" + key},
		{"key 带前导斜杠", "http://h:8080/api/v1/files", "http://h:8080/api/v1/files/" + key},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := URLWithBase(key, tc.base)
			if got != tc.want {
				t.Fatalf("url=%q，期望 %q", got, tc.want)
			}
		})
	}
	if got := URLWithBase("/"+key, "http://h:8080/api/v1/files"); got != "http://h:8080/api/v1/files/"+key {
		t.Fatalf("key 前导斜杠应被裁掉，实际=%q", got)
	}
}

// TestLocalStorageURLBehaviorUnchanged 保证 storage.URL()（其它调用方的既有口径）不被本次改动影响。
func TestLocalStorageURLBehaviorUnchanged(t *testing.T) {
	st, err := NewLocalStorage(t.TempDir(), "http://test/api/v1/files")
	if err != nil {
		t.Fatalf("NewLocalStorage: %v", err)
	}
	if got, want := st.URL("uploads/a/b.png"), "http://test/api/v1/files/uploads/a/b.png"; got != want {
		t.Fatalf("URL=%q，期望 %q", got, want)
	}

	st2, err := NewLocalStorage(t.TempDir(), "")
	if err != nil {
		t.Fatalf("NewLocalStorage: %v", err)
	}
	if got, want := st2.URL("uploads/a/b.png"), "/api/v1/files/uploads/a/b.png"; got != want {
		t.Fatalf("空 base URL=%q，期望 %q", got, want)
	}
}
