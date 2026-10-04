package database

import (
	"errors"
	"strings"

	"gorm.io/gorm"

	apperrors "ridgericetalk/core/errors"
)

// ClassifyError maps common GORM/database errors to application errors.
// It distinguishes retryable SQLite busy/locked errors, unique/constraint
// violations, and record-not-found errors from generic internal errors.
func ClassifyError(err error) *apperrors.AppError {
	if err == nil {
		return nil
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return apperrors.ErrNotFound
	}

	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "database is locked") ||
		strings.Contains(msg, "busy") ||
		strings.Contains(msg, "lock") {
		return apperrors.New(apperrors.SYSTEM_SERVICE_UNAVAILABLE, "database busy, please retry")
	}

	if strings.Contains(msg, "unique constraint") ||
		strings.Contains(msg, "duplicate") ||
		strings.Contains(msg, "constraint failed") {
		// 原始错误信息放到 Details，便于前端展示具体冲突原因（ChineseMessage 仍为通用提示）
		return apperrors.New(apperrors.SYSTEM_CONFLICT, "duplicate record").WithDetails(err.Error())
	}

	return apperrors.ErrInternal
}
