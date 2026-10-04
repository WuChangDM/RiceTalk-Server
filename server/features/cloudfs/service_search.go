package cloudfs

import (
	"path"
	"strings"

	"ridgericetalk/core/errors"
	"ridgericetalk/internal/model"
)

// cloudFSSearchLimit caps how many hits a single search returns. The design
// (DES-2026-0912-02 §4.4) deliberately keeps this a small LIKE query with a
// LIMIT instead of adding trigram indexes; file_name has no index, which is
// acceptable at self-hosted scale.
const cloudFSSearchLimit = 100

// Search finds files by name (GET /cloudfs/search).
//
// Scope: the whole space by default. When scopePath is given the match is
// limited to that folder and its subtree ("当前目录范围 + 递归"), which is the
// design's `path` query parameter. Folders are not searched — the design
// specifies SharedFileEntry.file_name — and results reuse the CloudFileItem
// shape the client already renders, plus `path` so a hit can be opened from
// anywhere in the tree.
//
// Permission: plain Space membership, exactly like List (no per-item ACL),
// so the search can never widen what a member can already see.
func (s *Service) Search(spaceID, query, scopePath, userID string) ([]CloudFileItem, error) {
	if err := s.requireMembership(spaceID, userID); err != nil {
		return nil, err
	}

	query = strings.TrimSpace(query)
	if query == "" {
		return nil, errors.ErrBadRequest.WithDetails("q is required")
	}

	// LOWER() on both sides keeps the match case-insensitive on PostgreSQL,
	// where LIKE is case-sensitive (SQLite's LIKE already folds ASCII).
	// escapeLike neutralises %/_/! so a query of "%" matches a literal percent
	// sign instead of every file.
	dbq := s.db.Model(&model.SharedFileEntry{}).
		Where("space_id = ?", spaceID).
		Where("LOWER(file_name) LIKE LOWER(?) ESCAPE '!'", "%"+escapeLike(query)+"%")

	if scope := path.Clean("/" + strings.TrimSpace(scopePath)); scopePath != "" && scope != "/" {
		dbq = dbq.Where("(file_path = ? OR file_path LIKE ? ESCAPE '!')", scope, escapeLike(scope)+"/%")
	}

	var rows []model.SharedFileEntry
	if err := dbq.Order("created_at DESC").Limit(cloudFSSearchLimit).Find(&rows).Error; err != nil {
		return nil, errors.ErrInternal
	}

	items := make([]CloudFileItem, 0, len(rows))
	for i := range rows {
		f := rows[i]
		items = append(items, CloudFileItem{
			ID:        f.ID,
			Name:      f.FileName,
			Type:      "file",
			Path:      f.FilePath,
			Size:      formatSize(f.FileSize),
			UploadBy:  s.displayName(f.UploadedBy),
			UpdatedAt: f.CreatedAt.Format("2006-01-02 15:04"),
		})
	}
	return items, nil
}
