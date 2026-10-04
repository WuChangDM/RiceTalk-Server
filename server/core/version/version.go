// Package version exposes the server's own semantic version so any package can
// reference it without import cycles (server and admin both need it).
package version

// Server is the server's own semantic version, bumped independently of the client
// releases. Bump it whenever the server gains features/changes that clients may need
// to detect (e.g. new APIs, migrations, or behaviors).
//
// Server 是服务端自身的语义版本号，独立于客户端发布节奏递增。它默认为 "0.3.0"，
// 但已改为变量以便在编译期通过 -ldflags -X 注入真实构建版本（例如版本 + 短 commit）。
// 未注入时保持默认值兜底，不影响既有构建流程。注入示例：
//
//	go build -ldflags "-X ridgericetalk/core/version.Server=0.3.0+abc1234" ./cmd/server
var Server = "0.3.0"

// Commit 是编译该二进制时所基于的源码提交短 SHA。
//
// 默认为空串（本地 `go build` 未注入时即为此值），由部署脚本在服务器上编译时用
// -ldflags -X 注入，例如：
//
//	go build -ldflags "-X ridgericetalk/core/version.Commit=abc1234" ./cmd/server
var Commit = ""

// BuildTime 是编译时间，建议使用 RFC3339 / UTC 时间戳（如 2026-09-12T08:00:00Z）。
//
// 默认为空串，同样由部署脚本通过 -ldflags -X 注入，例如：
//
//	go build -ldflags "-X ridgericetalk/core/version.BuildTime=2026-09-12T08:00:00Z" ./cmd/server
var BuildTime = ""

// MinClient is the oldest client version this server will accept. It tracks client
// compatibility, NOT the server version — keep it independent of Server.
const MinClient = "0.1.0"

// MaxClient is the newest client version this server is known to accept.
const MaxClient = "0.9.9"
