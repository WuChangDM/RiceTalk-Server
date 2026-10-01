// Package config 中的 livekit_render_test.go 对 livekit_render.go 暴露的
// 默认端口、占位符渲染、已存在文件保护与模板缺失等场景进行单元测试。
package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDefaultLiveKitPorts 验证 DefaultLiveKitPorts 返回推荐端口：
// WSPort=7880, UDPPort=7882, TCPPort=7881, PromPort=6789。
func TestDefaultLiveKitPorts(t *testing.T) {
	p := DefaultLiveKitPorts()
	if p.WSPort != 7880 {
		t.Errorf("WSPort = %d, want 7880", p.WSPort)
	}
	if p.UDPPort != 7882 {
		t.Errorf("UDPPort = %d, want 7882", p.UDPPort)
	}
	if p.TCPPort != 7881 {
		t.Errorf("TCPPort = %d, want 7881", p.TCPPort)
	}
	if p.PromPort != 6789 {
		t.Errorf("PromPort = %d, want 6789", p.PromPort)
	}
}

// TestRenderLiveKitConfig_Placeholders 验证 RenderLiveKitConfig 能正确替换
// 模板中的 6 个占位符，且输出中不残留未解析的 {{...}} 标记。
func TestRenderLiveKitConfig_Placeholders(t *testing.T) {
	tmpDir := t.TempDir()
	templatePath := filepath.Join(tmpDir, "livekit.yaml.template")
	outputPath := filepath.Join(tmpDir, "livekit.yaml")

	// 模板包含全部 6 个占位符。
	template := `api_key: {{LIVEKIT_API_KEY}}
api_secret: {{LIVEKIT_API_SECRET}}
node_ip: {{NODE_IP}}
port: {{WS_PORT}}
udp_port: {{UDP_PORT}}
prometheus_port: {{PROM_PORT}}
`
	if err := os.WriteFile(templatePath, []byte(template), 0o600); err != nil {
		t.Fatalf("写入模板失败: %v", err)
	}

	s := GenerateSecrets()
	ports := DefaultLiveKitPorts()
	if err := RenderLiveKitConfig(templatePath, outputPath, s, "1.2.3.4", ports); err != nil {
		t.Fatalf("RenderLiveKitConfig 失败: %v", err)
	}

	content, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("读取输出文件失败: %v", err)
	}
	str := string(content)

	// 逐一验证占位符已被替换为实际值。
	if !strings.Contains(str, s.LiveKitAPIKey) {
		t.Error("输出中未找到 LiveKitAPIKey")
	}
	if !strings.Contains(str, s.LiveKitAPISecret) {
		t.Error("输出中未找到 LiveKitAPISecret")
	}
	if !strings.Contains(str, "1.2.3.4") {
		t.Error("输出中未找到 NODE_IP")
	}
	if !strings.Contains(str, "7880") {
		t.Error("输出中未找到 WS_PORT")
	}
	if !strings.Contains(str, "7882") {
		t.Error("输出中未找到 UDP_PORT")
	}
	if !strings.Contains(str, "6789") {
		t.Error("输出中未找到 PROM_PORT")
	}
	// 确保没有剩余未解析的占位符。
	if strings.Contains(str, "{{") {
		t.Errorf("输出中残留未解析占位符: %q", str)
	}
}

// TestRenderLiveKitConfig_SkipExisting 验证当输出文件已存在时，
// RenderLiveKitConfig 不覆盖用户手动修改的配置。
func TestRenderLiveKitConfig_SkipExisting(t *testing.T) {
	tmpDir := t.TempDir()
	templatePath := filepath.Join(tmpDir, "livekit.yaml.template")
	outputPath := filepath.Join(tmpDir, "livekit.yaml")

	// 准备模板与已存在的输出文件（用户手动配置）。
	if err := os.WriteFile(templatePath, []byte("template content"), 0o600); err != nil {
		t.Fatalf("写入模板失败: %v", err)
	}
	existing := "user manual config"
	if err := os.WriteFile(outputPath, []byte(existing), 0o600); err != nil {
		t.Fatalf("写入已存在文件失败: %v", err)
	}

	s := GenerateSecrets()
	if err := RenderLiveKitConfig(templatePath, outputPath, s, "1.2.3.4", DefaultLiveKitPorts()); err != nil {
		t.Fatalf("RenderLiveKitConfig 失败: %v", err)
	}

	content, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("读取输出文件失败: %v", err)
	}
	if string(content) != existing {
		t.Errorf("已存在文件被覆盖: got %q, want %q", string(content), existing)
	}
}

// TestRenderLiveKitConfig_TemplateNotExist 验证模板文件不存在时
// RenderLiveKitConfig 返回错误，而非静默成功。
func TestRenderLiveKitConfig_TemplateNotExist(t *testing.T) {
	tmpDir := t.TempDir()
	outputPath := filepath.Join(tmpDir, "livekit.yaml")
	s := GenerateSecrets()
	err := RenderLiveKitConfig(filepath.Join(tmpDir, "nonexistent.template"), outputPath, s, "1.2.3.4", DefaultLiveKitPorts())
	if err == nil {
		t.Error("模板不存在时未返回错误")
	}
}

// TestRenderLiveKitConfig_DefaultTCPPort 验证使用默认端口渲染后，
// 输出文件中 tcp_port 为默认值 7881。
func TestRenderLiveKitConfig_DefaultTCPPort(t *testing.T) {
	tmpDir := t.TempDir()
	templatePath := filepath.Join(tmpDir, "livekit.yaml.template")
	outputPath := filepath.Join(tmpDir, "livekit.yaml")

	template := `port: {{WS_PORT}}
rtc:
  tcp_port: {{TCP_PORT}}
`
	if err := os.WriteFile(templatePath, []byte(template), 0o600); err != nil {
		t.Fatalf("写入模板失败: %v", err)
	}

	s := GenerateSecrets()
	ports := DefaultLiveKitPorts()
	if err := RenderLiveKitConfig(templatePath, outputPath, s, "1.2.3.4", ports); err != nil {
		t.Fatalf("RenderLiveKitConfig 失败: %v", err)
	}

	content, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("读取输出文件失败: %v", err)
	}
	str := string(content)

	want := "tcp_port: 7881"
	if !strings.Contains(str, want) {
		t.Errorf("默认 TCP_PORT 渲染失败: 输出中未找到 %q, got %q", want, str)
	}
	if strings.Contains(str, "{{TCP_PORT}}") {
		t.Errorf("输出中残留未解析的 {{TCP_PORT}} 占位符: %q", str)
	}
}

// TestRenderLiveKitConfig_CustomTCPPort 验证自定义 TCPPort 渲染后，
// 输出文件中 tcp_port 为自定义值。
func TestRenderLiveKitConfig_CustomTCPPort(t *testing.T) {
	tmpDir := t.TempDir()
	templatePath := filepath.Join(tmpDir, "livekit.yaml.template")
	outputPath := filepath.Join(tmpDir, "livekit.yaml")

	template := `port: {{WS_PORT}}
rtc:
  tcp_port: {{TCP_PORT}}
`
	if err := os.WriteFile(templatePath, []byte(template), 0o600); err != nil {
		t.Fatalf("写入模板失败: %v", err)
	}

	s := GenerateSecrets()
	ports := DefaultLiveKitPorts()
	ports.TCPPort = 18000 // 自定义 TCP 端口
	if err := RenderLiveKitConfig(templatePath, outputPath, s, "1.2.3.4", ports); err != nil {
		t.Fatalf("RenderLiveKitConfig 失败: %v", err)
	}

	content, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("读取输出文件失败: %v", err)
	}
	str := string(content)

	want := "tcp_port: 18000"
	if !strings.Contains(str, want) {
		t.Errorf("自定义 TCP_PORT 渲染失败: 输出中未找到 %q, got %q", want, str)
	}
	// 确保默认值 7881 不再出现于 tcp_port 行。
	if strings.Contains(str, "tcp_port: 7881") {
		t.Errorf("自定义 TCP_PORT 渲染异常: 输出中仍包含默认值 7881, got %q", str)
	}
	if strings.Contains(str, "{{TCP_PORT}}") {
		t.Errorf("输出中残留未解析的 {{TCP_PORT}} 占位符: %q", str)
	}
}

// TestRenderLiveKitConfig_BackwardCompatNoTCPPortPlaceholder 验证向后兼容：
// 当模板为不含 {{TCP_PORT}} 占位符的旧版本时，渲染不报错且保留原值。
func TestRenderLiveKitConfig_BackwardCompatNoTCPPortPlaceholder(t *testing.T) {
	tmpDir := t.TempDir()
	templatePath := filepath.Join(tmpDir, "livekit.yaml.template")
	outputPath := filepath.Join(tmpDir, "livekit.yaml")

	// 旧模板：tcp_port 直接写死为 7881，无 {{TCP_PORT}} 占位符。
	template := `port: {{WS_PORT}}
rtc:
  tcp_port: 7881
`
	if err := os.WriteFile(templatePath, []byte(template), 0o600); err != nil {
		t.Fatalf("写入模板失败: %v", err)
	}

	s := GenerateSecrets()
	ports := DefaultLiveKitPorts()
	// 即使 ports.TCPPort 是默认 7881，渲染也不应报错。
	if err := RenderLiveKitConfig(templatePath, outputPath, s, "1.2.3.4", ports); err != nil {
		t.Fatalf("RenderLiveKitConfig 失败: %v", err)
	}

	content, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatalf("读取输出文件失败: %v", err)
	}
	str := string(content)

	// 旧模板中 tcp_port: 7881 应保持不变。
	want := "tcp_port: 7881"
	if !strings.Contains(str, want) {
		t.Errorf("向后兼容失败: 输出中未找到保留的原值 %q, got %q", want, str)
	}
	// 确保其他占位符仍被替换（如 WS_PORT）。
	if strings.Contains(str, "{{WS_PORT}}") {
		t.Errorf("其他占位符未被替换: %q", str)
	}
}
