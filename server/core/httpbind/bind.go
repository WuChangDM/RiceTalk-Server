// Package httpbind 提供 HTTP 请求绑定的共享 helper，弥补 gin ShouldBindJSON
// 对"空请求体"与"非法 JSON"不加区分的不足。
//
// SEC-001: 4 处 handler 此前直接忽略 c.ShouldBindJSON 的返回错误，
// 导致非法 JSON 被静默当作空请求体处理。本 helper 仅容忍 io.EOF（空请求体），
// 对其他解析错误（json.SyntaxError / json.UnmarshalTypeError 等）原样返回，
// 调用方应在 err 非 nil 时返回 400。
package httpbind

import (
	"errors"
	"io"

	"github.com/gin-gonic/gin"
)

// BindJSONAllowEmpty 绑定 JSON 请求体，允许空请求体（io.EOF），
// 但对其他解析错误（非法 JSON、类型不匹配）返回 error。
//
// 使用场景：业务上接受空请求体（如 Logout 走 cookie 兜底、PinMessage 缺省置顶），
// 但仍需拒绝格式错误的 JSON。调用方 SHOULD 在 err 非 nil 时返回 400。
//
// 注意：obj 在返回 nil error 时不一定被填充；若请求体为空，obj 保持零值。
func BindJSONAllowEmpty(c *gin.Context, obj interface{}) error {
	err := c.ShouldBindJSON(obj)
	if err == nil {
		return nil
	}
	// io.EOF 表示请求体为空（Content-Length: 0 或无 body），视为合法的"空请求体"
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}
