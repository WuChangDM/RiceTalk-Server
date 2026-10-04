package cloudfs

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"time"

	"gorm.io/gorm"

	"ridgericetalk/core/errors"
	"ridgericetalk/internal/database"
	"ridgericetalk/internal/model"
)

// Trash support (DES-2026-0912-02 §4.3).
//
// Deletion in CloudFS was always a GORM soft delete (rows keep deleted_at) but
// the content was os.Remove'd, so a "deleted" item was unrecoverable while its
// tombstone row stayed behind. This file completes the semantics:
//
//   * content is parked in <baseDir>/.trash/<physicalName> instead of being
//     deleted, so it can be restored;
//   * GET  /cloudfs/trash            lists the (top-level) deleted entries;
//   * POST /cloudfs/trash/restore    clears deleted_at and un-parks the content;
//   * DELETE /cloudfs/trash          purges one entry for good;
//   * DELETE /cloudfs/trash/all      empties the trash;
//   * a background job purges entries older than CloudFSTrashRetentionDays.
//
// Scope rule: the trash addresses TOP-LEVEL entries only — an entry whose parent
// folder is itself in the trash is listed and restored together with that
// folder, never on its own (same as a desktop recycle bin). This keeps a
// restore from resurrecting a child into a folder that does not exist yet.
//
// Quota note (design §4.3): GetUsage sums live SharedFileEntry rows, so trash
// content does not count against the quota even though it still occupies disk
// until the retention window expires.

// TrashItem is one soft-deleted entry as returned by GET /cloudfs/trash.
type TrashItem struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Type      string `json:"type"` // folder | file
	Path      string `json:"path"`
	Size      string `json:"size,omitempty"`
	UploadBy  string `json:"uploadBy,omitempty"`
	DeletedAt string `json:"deletedAt"`
	ExpiresAt string `json:"expiresAt,omitempty"`
}

const cloudFSDefaultTrashRetentionDays = 30

// trashRetentionDays returns the configured retention, falling back to 30 days.
func (s *Service) trashRetentionDays() int {
	if days := s.cfg.CloudFSTrashRetentionDays; days > 0 {
		return days
	}
	return cloudFSDefaultTrashRetentionDays
}

// trashDir is where soft-deleted content is parked. It lives under baseDir so
// restore/purge resolve physical names against a single root; it is a
// directory, so it can never collide with a stored file (physical names always
// contain "_" and never start with ".").
func (s *Service) trashDir() string {
	return filepath.Join(s.baseDir(), ".trash")
}

// moveToTrash relocates stored content into the trash directory. Missing content
// is not an error (legacy rows may have no physical file, or the content may
// already be parked), and restore/purge look in both locations.
func (s *Service) moveToTrash(physicalName string) error {
	if physicalName == "" {
		return nil
	}
	src := filepath.Join(s.baseDir(), physicalName)
	if _, err := os.Stat(src); err != nil {
		return nil
	}
	if err := os.MkdirAll(s.trashDir(), 0755); err != nil {
		return err
	}
	return os.Rename(src, filepath.Join(s.trashDir(), physicalName))
}

// restoreFromTrash moves parked content back next to its siblings.
func (s *Service) restoreFromTrash(physicalName string) {
	if physicalName == "" {
		return
	}
	src := filepath.Join(s.trashDir(), physicalName)
	if _, err := os.Stat(src); err != nil {
		return // nothing parked; the content is (still) at its original path
	}
	_ = os.Rename(src, filepath.Join(s.baseDir(), physicalName))
}

// removeTrashContent deletes parked content for good, tolerating both locations
// (a failed park leaves the content at its original path).
func (s *Service) removeTrashContent(physicalName string) {
	if physicalName == "" {
		return
	}
	os.Remove(filepath.Join(s.trashDir(), physicalName))
	os.Remove(filepath.Join(s.baseDir(), physicalName))
}

// ListTrash returns the space's top-level deleted folders and files, newest
// first.
func (s *Service) ListTrash(spaceID, userID string) ([]TrashItem, error) {
	if err := s.requireMembership(spaceID, userID); err != nil {
		return nil, err
	}

	// One sweep of every folder row (live and deleted) answers "is the parent
	// deleted?" without a query per item.
	allFolders := map[string]model.SharedFolder{}
	var folders []model.SharedFolder
	if err := s.db.Unscoped().Where("space_id = ?", spaceID).Find(&folders).Error; err != nil {
		return nil, errors.ErrInternal
	}
	for i := range folders {
		allFolders[folders[i].ID] = folders[i]
	}

	var files []model.SharedFileEntry
	if err := s.db.Unscoped().
		Where("space_id = ? AND deleted_at IS NOT NULL", spaceID).
		Order("deleted_at DESC").Find(&files).Error; err != nil {
		return nil, errors.ErrInternal
	}

	items := make([]TrashItem, 0, len(folders)+len(files))
	for i := range folders {
		f := folders[i]
		if !f.DeletedAt.Valid {
			continue
		}
		if f.ParentID != nil {
			if parent, ok := allFolders[*f.ParentID]; ok && parent.DeletedAt.Valid {
				continue // nested: listed with its deleted ancestor instead
			}
		}
		items = append(items, TrashItem{
			ID:        f.ID,
			Name:      f.Name,
			Type:      "folder",
			Path:      f.Path,
			DeletedAt: f.DeletedAt.Time.Format("2006-01-02 15:04"),
			ExpiresAt: s.trashExpiry(f.DeletedAt.Time),
		})
	}
	for i := range files {
		f := files[i]
		if f.FolderID != "" {
			if parent, ok := allFolders[f.FolderID]; ok && parent.DeletedAt.Valid {
				continue
			}
		}
		items = append(items, TrashItem{
			ID:        f.ID,
			Name:      f.FileName,
			Type:      "file",
			Path:      f.FilePath,
			Size:      formatSize(f.FileSize),
			UploadBy:  s.displayName(f.UploadedBy),
			DeletedAt: f.DeletedAt.Time.Format("2006-01-02 15:04"),
			ExpiresAt: s.trashExpiry(f.DeletedAt.Time),
		})
	}

	sort.SliceStable(items, func(i, j int) bool { return items[i].DeletedAt > items[j].DeletedAt })
	return items, nil
}

func (s *Service) trashExpiry(deletedAt time.Time) string {
	return deletedAt.AddDate(0, 0, s.trashRetentionDays()).Format("2006-01-02 15:04")
}

func (s *Service) displayName(userID string) string {
	if userID == "" {
		return ""
	}
	var u model.User
	if err := s.db.Select("display_name, username").First(&u, "id = ?", userID).Error; err != nil {
		return ""
	}
	if u.DisplayName != "" {
		return u.DisplayName
	}
	return u.Username
}

// RestoreTrash revives a top-level deleted entry in place. The original path
// must be free again — if something already occupies it the caller gets
// CLOUDFS_ALREADY_EXISTS, exactly like the rename/move conflict rule.
func (s *Service) RestoreTrash(spaceID, id, itemPath, userID string) (*TrashItem, error) {
	if err := s.requireMembership(spaceID, userID); err != nil {
		return nil, err
	}

	var restored *TrashItem
	err := s.db.Transaction(func(tx *gorm.DB) error {
		file, folder, err := resolveTrashItem(tx, spaceID, id, itemPath)
		if err != nil {
			return err
		}

		// Same permission rule as Delete: the item's owner or a Space admin.
		if file != nil {
			if file.UploadedBy != userID && !isSpaceAdminTx(tx, spaceID, userID) {
				return errors.ErrForbidden
			}
			if err := s.restoreTrashFile(tx, spaceID, file); err != nil {
				return err
			}
			restored = &TrashItem{ID: file.ID, Name: file.FileName, Type: "file", Path: file.FilePath}
			return nil
		}
		if folder.OwnerID != userID && !isSpaceAdminTx(tx, spaceID, userID) {
			return errors.ErrForbidden
		}
		if err := s.restoreTrashFolder(tx, spaceID, folder); err != nil {
			return err
		}
		restored = &TrashItem{ID: folder.ID, Name: folder.Name, Type: "folder", Path: folder.Path}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return restored, nil
}

func (s *Service) restoreTrashFile(tx *gorm.DB, spaceID string, file *model.SharedFileEntry) error {
	owner, err := pathOwner(tx, spaceID, file.FilePath)
	if err != nil {
		return err
	}
	if owner != "" {
		return errors.New(errors.CLOUDFS_ALREADY_EXISTS, "an item with the same name already exists")
	}
	if err := tx.Unscoped().Model(&model.SharedFileEntry{}).Where("id = ?", file.ID).
		Update("deleted_at", nil).Error; err != nil {
		return database.ClassifyError(err)
	}
	s.restoreFromTrash(file.PhysicalName)
	return nil
}

// restoreTrashFolder revives the folder and its whole deleted subtree, refusing
// atomically when any destination path is occupied by a live row.
func (s *Service) restoreTrashFolder(tx *gorm.DB, spaceID string, folder *model.SharedFolder) error {
	subFolders, subFiles, err := deletedSubtree(tx, spaceID, folder.Path)
	if err != nil {
		return err
	}

	owned := map[string]bool{folder.ID: true}
	destinations := []string{folder.Path}
	for i := range subFolders {
		owned[subFolders[i].ID] = true
		destinations = append(destinations, subFolders[i].Path)
	}
	for i := range subFiles {
		owned[subFiles[i].ID] = true
		destinations = append(destinations, subFiles[i].FilePath)
	}
	for _, p := range destinations {
		owner, err := pathOwner(tx, spaceID, p)
		if err != nil {
			return err
		}
		if owner != "" && !owned[owner] {
			return errors.New(errors.CLOUDFS_ALREADY_EXISTS, "an item with the same name already exists")
		}
	}

	pattern := escapeLike(folder.Path) + "/%"
	if err := tx.Unscoped().Model(&model.SharedFolder{}).
		Where("space_id = ? AND deleted_at IS NOT NULL AND (path = ? OR path LIKE ? ESCAPE '!')",
			spaceID, folder.Path, pattern).
		Update("deleted_at", nil).Error; err != nil {
		return database.ClassifyError(err)
	}
	if err := tx.Unscoped().Model(&model.SharedFileEntry{}).
		Where("space_id = ? AND deleted_at IS NOT NULL AND (file_path = ? OR file_path LIKE ? ESCAPE '!')",
			spaceID, folder.Path, pattern).
		Update("deleted_at", nil).Error; err != nil {
		return database.ClassifyError(err)
	}
	for i := range subFiles {
		s.restoreFromTrash(subFiles[i].PhysicalName)
	}
	return nil
}

// PurgeTrash removes one top-level entry (and, for a folder, its whole deleted
// subtree) for good: rows plus parked content plus FileMetadata rows.
func (s *Service) PurgeTrash(spaceID, id, itemPath, userID string) error {
	if err := s.requireMembership(spaceID, userID); err != nil {
		return err
	}

	var content []string
	err := s.db.Transaction(func(tx *gorm.DB) error {
		file, folder, err := resolveTrashItem(tx, spaceID, id, itemPath)
		if err != nil {
			return err
		}
		if file != nil {
			if file.UploadedBy != userID && !isSpaceAdminTx(tx, spaceID, userID) {
				return errors.ErrForbidden
			}
			if err := purgeFileRowTx(s, tx, file); err != nil {
				return err
			}
			content = append(content, file.PhysicalName)
			return nil
		}
		if folder.OwnerID != userID && !isSpaceAdminTx(tx, spaceID, userID) {
			return errors.ErrForbidden
		}
		names, err := purgeFolderRowsTx(s, tx, spaceID, folder)
		content = append(content, names...)
		return err
	})
	if err != nil {
		return err
	}
	for _, name := range content {
		s.removeTrashContent(name)
	}
	return nil
}

// EmptyTrash removes every deleted row of the space. Unlike the single-entry
// operations it does not restrict itself to top-level entries — everything in
// the trash is going away anyway.
func (s *Service) EmptyTrash(spaceID, userID string) (int, error) {
	if err := s.requireMembership(spaceID, userID); err != nil {
		return 0, err
	}

	var content []string
	count := 0
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var files []model.SharedFileEntry
		if err := tx.Unscoped().
			Where("space_id = ? AND deleted_at IS NOT NULL", spaceID).Find(&files).Error; err != nil {
			return err
		}
		for i := range files {
			if err := purgeFileRowTx(s, tx, &files[i]); err != nil {
				return err
			}
			content = append(content, files[i].PhysicalName)
			count++
		}

		res := tx.Unscoped().Where("space_id = ? AND deleted_at IS NOT NULL", spaceID).
			Delete(&model.SharedFolder{})
		if res.Error != nil {
			return res.Error
		}
		count += int(res.RowsAffected)
		return nil
	})
	if err != nil {
		return 0, err
	}
	for _, name := range content {
		s.removeTrashContent(name)
	}
	return count, nil
}

// PurgeExpiredTrash permanently removes everything deleted longer than the
// configured retention. Idempotent and re-entrant: rows that disappeared
// meanwhile are skipped, and a missing physical file is not an error.
func (s *Service) PurgeExpiredTrash(now time.Time) (int, error) {
	cutoff := now.AddDate(0, 0, -s.trashRetentionDays())

	var content []string
	count := 0
	err := s.db.Transaction(func(tx *gorm.DB) error {
		// Ordering by path puts ancestors before descendants, so an expired
		// folder is purged once (with its subtree) instead of once per level.
		var folders []model.SharedFolder
		if err := tx.Unscoped().
			Where("deleted_at IS NOT NULL AND deleted_at < ?", cutoff).
			Order("path").Find(&folders).Error; err != nil {
			return err
		}
		for i := range folders {
			f := folders[i]
			alive, err := rowExists(tx, &model.SharedFolder{}, f.ID)
			if err != nil {
				return err
			}
			if !alive {
				continue // already purged as part of an ancestor
			}
			names, err := purgeFolderRowsTx(s, tx, f.SpaceID, &f)
			if err != nil {
				return err
			}
			content = append(content, names...)
			count++
		}

		var files []model.SharedFileEntry
		if err := tx.Unscoped().
			Where("deleted_at IS NOT NULL AND deleted_at < ?", cutoff).
			Order("id").Find(&files).Error; err != nil {
			return err
		}
		for i := range files {
			f := files[i]
			alive, err := rowExists(tx, &model.SharedFileEntry{}, f.ID)
			if err != nil {
				return err
			}
			if !alive {
				continue
			}
			if err := purgeFileRowTx(s, tx, &f); err != nil {
				return err
			}
			content = append(content, f.PhysicalName)
			count++
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	for _, name := range content {
		s.removeTrashContent(name)
	}
	return count, nil
}

// StartTrashCleanupLoop launches the retention job, mirroring the other feature
// loops (minigames/schedule): one sweep at startup, then daily until ctx ends.
func (s *Service) StartTrashCleanupLoop(ctx context.Context) {
	run := func() {
		n, err := s.PurgeExpiredTrash(time.Now())
		if err != nil {
			fmt.Fprintf(os.Stderr, "WARN: cloudfs trash cleanup failed: %v\n", err)
			return
		}
		if n > 0 {
			fmt.Fprintf(os.Stderr, "INFO: cloudfs trash cleanup purged %d expired entries\n", n)
		}
	}

	run()
	go func() {
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				run()
			}
		}
	}()
}

// resolveTrashItem locates exactly one TOP-LEVEL soft-deleted entry by id (or,
// when id is empty, by logical path). Files are checked before folders to stay
// consistent with Service.Delete.
func resolveTrashItem(tx *gorm.DB, spaceID, id, itemPath string) (*model.SharedFileEntry, *model.SharedFolder, error) {
	if id != "" {
		var file model.SharedFileEntry
		if err := tx.Unscoped().
			Where("id = ? AND space_id = ? AND deleted_at IS NOT NULL", id, spaceID).
			Limit(1).Find(&file).Error; err != nil {
			return nil, nil, err
		}
		if file.ID != "" {
			return topLevelFile(tx, &file)
		}
		var folder model.SharedFolder
		if err := tx.Unscoped().
			Where("id = ? AND space_id = ? AND deleted_at IS NOT NULL", id, spaceID).
			Limit(1).Find(&folder).Error; err != nil {
			return nil, nil, err
		}
		if folder.ID != "" {
			return topLevelFolder(tx, &folder)
		}
		return nil, nil, errors.ErrNotFound
	}

	if itemPath == "" {
		return nil, nil, errors.ErrBadRequest.WithDetails("id or path is required")
	}
	itemPath = path.Clean("/" + itemPath)

	var file model.SharedFileEntry
	if err := tx.Unscoped().
		Where("space_id = ? AND file_path = ? AND deleted_at IS NOT NULL", spaceID, itemPath).
		Limit(1).Find(&file).Error; err != nil {
		return nil, nil, err
	}
	if file.ID != "" {
		return topLevelFile(tx, &file)
	}

	var folder model.SharedFolder
	if err := tx.Unscoped().
		Where("space_id = ? AND path = ? AND deleted_at IS NOT NULL", spaceID, itemPath).
		Limit(1).Find(&folder).Error; err != nil {
		return nil, nil, err
	}
	if folder.ID != "" {
		return topLevelFolder(tx, &folder)
	}
	return nil, nil, errors.ErrNotFound
}

func topLevelFile(tx *gorm.DB, file *model.SharedFileEntry) (*model.SharedFileEntry, *model.SharedFolder, error) {
	if file.FolderID != "" {
		var parent model.SharedFolder
		if err := tx.Unscoped().Select("id, deleted_at").Where("id = ?", file.FolderID).
			Limit(1).Find(&parent).Error; err != nil {
			return nil, nil, err
		}
		if parent.DeletedAt.Valid {
			return nil, nil, errors.ErrBadRequest.WithDetails("restore or purge the parent folder first")
		}
	}
	return file, nil, nil
}

func topLevelFolder(tx *gorm.DB, folder *model.SharedFolder) (*model.SharedFileEntry, *model.SharedFolder, error) {
	if folder.ParentID != nil {
		var parent model.SharedFolder
		if err := tx.Unscoped().Select("id, deleted_at").Where("id = ?", *folder.ParentID).
			Limit(1).Find(&parent).Error; err != nil {
			return nil, nil, err
		}
		if parent.DeletedAt.Valid {
			return nil, nil, errors.ErrBadRequest.WithDetails("restore or purge the parent folder first")
		}
	}
	return nil, folder, nil
}

// deletedSubtree returns every still-deleted folder/file row under prefix.
func deletedSubtree(tx *gorm.DB, spaceID, prefix string) ([]model.SharedFolder, []model.SharedFileEntry, error) {
	pattern := escapeLike(prefix) + "/%"

	var subFolders []model.SharedFolder
	if err := tx.Unscoped().
		Where("space_id = ? AND deleted_at IS NOT NULL AND (path = ? OR path LIKE ? ESCAPE '!')",
			spaceID, prefix, pattern).
		Find(&subFolders).Error; err != nil {
		return nil, nil, err
	}

	var subFiles []model.SharedFileEntry
	if err := tx.Unscoped().
		Where("space_id = ? AND deleted_at IS NOT NULL AND (file_path = ? OR file_path LIKE ? ESCAPE '!')",
			spaceID, prefix, pattern).
		Find(&subFiles).Error; err != nil {
		return nil, nil, err
	}
	return subFolders, subFiles, nil
}

// purgeFileRowTx hard-deletes one file row and its FileMetadata row. The
// physical content is left to the caller so nothing is removed inside a
// transaction that might still roll back.
func purgeFileRowTx(s *Service, tx *gorm.DB, row *model.SharedFileEntry) error {
	if row.PhysicalName != "" {
		physicalPath := filepath.Join(s.baseDir(), row.PhysicalName)
		if err := tx.Where("file_path = ?", physicalPath).Delete(&model.FileMetadata{}).Error; err != nil {
			return err
		}
	}
	return tx.Unscoped().Where("id = ?", row.ID).Delete(&model.SharedFileEntry{}).Error
}

// purgeFolderRowsTx hard-deletes a deleted folder plus every still-deleted
// descendant, returning the physical names to unlink afterwards.
func purgeFolderRowsTx(s *Service, tx *gorm.DB, spaceID string, folder *model.SharedFolder) ([]string, error) {
	_, subFiles, err := deletedSubtree(tx, spaceID, folder.Path)
	if err != nil {
		return nil, err
	}

	names := make([]string, 0, len(subFiles)+1)
	for i := range subFiles {
		if err := purgeFileRowTx(s, tx, &subFiles[i]); err != nil {
			return nil, err
		}
		names = append(names, subFiles[i].PhysicalName)
	}

	pattern := escapeLike(folder.Path) + "/%"
	res := tx.Unscoped().
		Where("space_id = ? AND deleted_at IS NOT NULL AND (path = ? OR path LIKE ? ESCAPE '!')",
			spaceID, folder.Path, pattern).
		Delete(&model.SharedFolder{})
	if res.Error != nil {
		return nil, res.Error
	}
	return names, nil
}

// rowExists reports whether a row is still present, ignoring soft-delete state.
func rowExists(tx *gorm.DB, model interface{}, id string) (bool, error) {
	var n int64
	if err := tx.Unscoped().Model(model).Where("id = ?", id).Count(&n).Error; err != nil {
		return false, err
	}
	return n > 0, nil
}
