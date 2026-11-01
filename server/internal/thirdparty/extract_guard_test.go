package thirdparty

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// 1.9.5 task-138（zip bomb 防护）：解压前预检条目数/解压后总量，解压中按累计
// 写入量兜底，路径穿越直接报错（不再静默跳过）。

func writeZip(t *testing.T, path string, entries map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("建 zip 失败: %v", err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for name, content := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("写 zip 条目失败: %v", err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("写 zip 内容失败: %v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("关闭 zip 失败: %v", err)
	}
}

func writeTarGz(t *testing.T, path string, entries map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("建 tar.gz 失败: %v", err)
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for name, content := range entries {
		if err := tw.WriteHeader(&tar.Header{
			Name:     name,
			Mode:     0o644,
			Size:     int64(len(content)),
			Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatalf("写 tar header 失败: %v", err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("写 tar 内容失败: %v", err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatalf("关闭 tar 失败: %v", err)
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("关闭 gzip 失败: %v", err)
	}
}

// TestExtractBudgetLimits 预算器的两个上限（条目数 / 累计字节）。
func TestExtractBudgetLimits(t *testing.T) {
	b := &extractBudget{}
	for i := 0; i < extractMaxEntries; i++ {
		if err := b.addEntry(); err != nil {
			t.Fatalf("第 %d 个条目不应超限: %v", i+1, err)
		}
	}
	if err := b.addEntry(); !errors.Is(err, errExtractTooMany) {
		t.Fatalf("超过条目上限应返回 errExtractTooMany，实际 %v", err)
	}

	b = &extractBudget{}
	if err := b.addBytes(extractMaxTotalBytes); err != nil {
		t.Fatalf("刚好到上限不应报错: %v", err)
	}
	if err := b.addBytes(1); !errors.Is(err, errExtractTooLarge) {
		t.Fatalf("超过字节上限应返回 errExtractTooLarge，实际 %v", err)
	}
}

// TestExtractZipRejectsTooManyEntries 条目数超限（10001 个）必须在解压前拒绝。
func TestExtractZipRejectsTooManyEntries(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "many.zip")
	f, err := os.Create(src)
	if err != nil {
		t.Fatalf("建 zip 失败: %v", err)
	}
	zw := zip.NewWriter(f)
	for i := 0; i < extractMaxEntries+1; i++ {
		w, err := zw.Create(fmt.Sprintf("f-%05d.txt", i))
		if err != nil {
			t.Fatalf("写 zip 条目失败: %v", err)
		}
		if _, err := w.Write([]byte("x")); err != nil {
			t.Fatalf("写 zip 内容失败: %v", err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("关闭 zip 失败: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("关闭文件失败: %v", err)
	}

	dst := filepath.Join(dir, "out")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatalf("建输出目录失败: %v", err)
	}
	err = extractZip(src, dst)
	if !errors.Is(err, errExtractTooMany) {
		t.Fatalf("条目数超限应返回 errExtractTooMany，实际 %v", err)
	}
}

// TestExtractZipRejectsTraversal 路径穿越条目直接报错，不再静默跳过。
func TestExtractZipRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "evil.zip")
	writeZip(t, src, map[string]string{"../evil.txt": "boom"})

	dst := filepath.Join(dir, "out")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatalf("建输出目录失败: %v", err)
	}
	err := extractZip(src, dst)
	if !errors.Is(err, errExtractTraversal) {
		t.Fatalf("路径穿越应返回 errExtractTraversal，实际 %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(dir, "evil.txt")); statErr == nil {
		t.Fatalf("穿越文件不应被写出到目标目录之外")
	}
}

// TestExtractTarGzRejectsTraversal tar.gz 同样拒绝路径穿越。
func TestExtractTarGzRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "evil.tar.gz")
	writeTarGz(t, src, map[string]string{"../evil.txt": "boom"})

	dst := filepath.Join(dir, "out")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatalf("建输出目录失败: %v", err)
	}
	if err := extractTarGz(src, dst); !errors.Is(err, errExtractTraversal) {
		t.Fatalf("路径穿越应返回 errExtractTraversal，实际 %v", err)
	}
}

// TestExtractZipHappyPath 正常包仍能正确解出内容（回归）。
func TestExtractZipHappyPath(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "ok.zip")
	writeZip(t, src, map[string]string{"bin/tool.txt": "hello", "README.txt": "readme"})

	dst := filepath.Join(dir, "out")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatalf("建输出目录失败: %v", err)
	}
	if err := extractZip(src, dst); err != nil {
		t.Fatalf("正常 zip 解压失败: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dst, "bin", "tool.txt"))
	if err != nil {
		t.Fatalf("读取解压结果失败: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("解压内容不符，期望 hello，实际 %q", string(got))
	}
}

// TestExtractTarGzHappyPath 正常 tar.gz 仍能正确解出内容（回归）。
func TestExtractTarGzHappyPath(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "ok.tar.gz")
	writeTarGz(t, src, map[string]string{"bin/tool.txt": "hello"})

	dst := filepath.Join(dir, "out")
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatalf("建输出目录失败: %v", err)
	}
	if err := extractTarGz(src, dst); err != nil {
		t.Fatalf("正常 tar.gz 解压失败: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dst, "bin", "tool.txt"))
	if err != nil {
		t.Fatalf("读取解压结果失败: %v", err)
	}
	if string(got) != "hello" {
		t.Fatalf("解压内容不符，期望 hello，实际 %q", string(got))
	}
}
