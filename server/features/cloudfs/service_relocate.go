package cloudfs

import (
	"fmt"
	"path"
	"strings"
	"unicode/utf8"

	"gorm.io/gorm"

	"ridgericetalk/core/errors"
	"ridgericetalk/internal/database"
	"ridgericetalk/internal/model"
)

// RelocateResult describes the outcome of a rename/move operation.
type RelocateResult struct {
	Kind    string `json:"kind"` // "file" | "folder"
	Name    string `json:"name"`
	OldPath string `json:"oldPath"`
	NewPath string `json:"newPath"`
}

// Rename renames a file or folder inside its current parent folder
// (PATCH /cloudfs/rename). Physical file names are decoupled from the logical
// name (SharedFileEntry.PhysicalName), so only the DB row moves.
func (s *Service) Rename(spaceID, itemPath, newName, userID string) (*RelocateResult, error) {
	return s.relocate(spaceID, itemPath, &newName, nil, userID)
}

// Move moves a file or folder under targetPath, keeping its current name
// (POST /cloudfs/move).
func (s *Service) Move(spaceID, itemPath, targetPath, userID string) (*RelocateResult, error) {
	return s.relocate(spaceID, itemPath, nil, &targetPath, userID)
}

// relocate implements both operations. A nil newName keeps the current name; a
// nil targetParent keeps the current parent (pure rename). A non-nil
// targetParent also re-parents the item (move).
//
// Permissions mirror Delete: the item's UploadedBy/OwnerID or a Space admin.
// Writing into a target folder additionally requires the same ownership the
// upload path enforces. Folder moves rewrite the whole subtree's path prefix in
// a single transaction, because SharedFolder.Path is the authoritative lookup
// key used by List/Upload/Download.
func (s *Service) relocate(spaceID, itemPath string, newName, targetParent *string, userID string) (*RelocateResult, error) {
	if err := s.requireMembership(spaceID, userID); err != nil {
		return nil, err
	}

	itemPath = path.Clean("/" + itemPath)
	if itemPath == "/" {
		return nil, errors.ErrBadRequest.WithDetails("the root folder cannot be renamed or moved")
	}

	var result *RelocateResult
	err := s.db.Transaction(func(tx *gorm.DB) error {
		file, folder, err := resolveCloudFSItem(tx, spaceID, itemPath)
		if err != nil {
			return err
		}

		// Permission on the item itself (same rule as Delete).
		if file != nil {
			if file.UploadedBy != userID && !isSpaceAdminTx(tx, spaceID, userID) {
				return errors.ErrForbidden
			}
		} else if folder.OwnerID != userID && !isSpaceAdminTx(tx, spaceID, userID) {
			return errors.ErrForbidden
		}

		name := path.Base(itemPath)
		if newName != nil {
			name = *newName
		}
		maxLen := 256
		if folder != nil {
			maxLen = 128 // SharedFolder.Name size:128
		}
		if err := validateCloudFSName(name, maxLen); err != nil {
			return err
		}

		// Resolve the destination parent.
		reparent := targetParent != nil
		parentPath := path.Dir(itemPath)
		if reparent {
			parentPath = path.Clean("/" + *targetParent)
			if parentPath == "." {
				parentPath = "/"
			}
		}

		var targetFolderID string
		var targetParentPtr *string
		if reparent {
			if parentPath != "/" {
				var tf model.SharedFolder
				if err := tx.Where("space_id = ? AND path = ?", spaceID, parentPath).
					Limit(1).Find(&tf).Error; err != nil {
					return err
				}
				if tf.ID == "" {
					return errors.ErrBadRequest.WithDetails("target folder not found")
				}
				if tf.OwnerID != userID && !isSpaceAdminTx(tx, spaceID, userID) {
					return errors.New(errors.CLOUDFS_PERMISSION_DENIED, "no permission to write to this folder")
				}
				targetFolderID = tf.ID
				targetParentPtr = &tf.ID
			}
		}

		newPath := path.Join(parentPath, name)
		if newPath == itemPath {
			// Nothing to do (same name and parent) — report success idempotently.
			result = &RelocateResult{Kind: itemKind(folder, file), Name: name, OldPath: itemPath, NewPath: newPath}
			return nil
		}

		if folder != nil {
			// Moving a folder into itself or one of its descendants would create
			// a cycle and corrupt path-prefix rewriting.
			if reparent && (parentPath == itemPath || strings.HasPrefix(parentPath, itemPath+"/")) {
				return errors.ErrBadRequest.WithDetails("cannot move a folder into itself or its own subfolder")
			}
			if err := s.relocateFolder(tx, spaceID, folder, name, newPath, targetParentPtr, reparent); err != nil {
				return err
			}
		} else {
			if err := s.relocateFile(tx, spaceID, file, name, newPath, targetFolderID, reparent); err != nil {
				return err
			}
		}

		result = &RelocateResult{Kind: itemKind(folder, file), Name: name, OldPath: itemPath, NewPath: newPath}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

func itemKind(folder *model.SharedFolder, file *model.SharedFileEntry) string {
	if folder != nil {
		return "folder"
	}
	if file != nil {
		return "file"
	}
	return ""
}

// resolveCloudFSItem locates exactly one live item by its logical path. Files
// are checked before folders to stay consistent with Service.Delete.
func resolveCloudFSItem(tx *gorm.DB, spaceID, itemPath string) (*model.SharedFileEntry, *model.SharedFolder, error) {
	var file model.SharedFileEntry
	if err := tx.Where("space_id = ? AND file_path = ?", spaceID, itemPath).
		Limit(1).Find(&file).Error; err != nil {
		return nil, nil, err
	}
	if file.ID != "" {
		return &file, nil, nil
	}

	var folder model.SharedFolder
	if err := tx.Where("space_id = ? AND path = ?", spaceID, itemPath).
		Limit(1).Find(&folder).Error; err != nil {
		return nil, nil, err
	}
	if folder.ID != "" {
		return nil, &folder, nil
	}

	return nil, nil, errors.ErrNotFound
}

// relocateFile updates a file's logical name/path (and folder when re-parented).
func (s *Service) relocateFile(tx *gorm.DB, spaceID string, file *model.SharedFileEntry, name, newPath, targetFolderID string, reparent bool) error {
	ownerID, err := pathOwner(tx, spaceID, newPath)
	if err != nil {
		return err
	}
	if ownerID != "" && ownerID != file.ID {
		return errors.New(errors.CLOUDFS_ALREADY_EXISTS, "an item with the same name already exists")
	}

	updates := map[string]interface{}{
		"file_name": name,
		"file_path": newPath,
	}
	if reparent {
		updates["folder_id"] = targetFolderID
	}
	if err := tx.Model(&model.SharedFileEntry{}).Where("id = ?", file.ID).Updates(updates).Error; err != nil {
		return database.ClassifyError(err)
	}
	return nil
}

// relocateFolder updates a folder and rewrites the path prefix of every
// descendant folder and file entry in one transaction. When anything conflicted
// the whole transaction is rolled back, so a partial rename can never happen.
func (s *Service) relocateFolder(tx *gorm.DB, spaceID string, folder *model.SharedFolder, name, newPath string, targetParentPtr *string, reparent bool) error {
	oldPrefix := folder.Path

	subFolders, subFiles, err := collectSubtree(tx, spaceID, oldPrefix)
	if err != nil {
		return err
	}

	// Collision pre-check against every destination path before touching a row.
	owned := map[string]bool{folder.ID: true}
	for _, f := range subFolders {
		owned[f.ID] = true
	}
	for _, f := range subFiles {
		owned[f.ID] = true
	}
	for _, p := range destinationPaths(oldPrefix, newPath, subFolders, subFiles) {
		ownerID, err := pathOwner(tx, spaceID, p)
		if err != nil {
			return err
		}
		if ownerID != "" && !owned[ownerID] {
			return errors.New(errors.CLOUDFS_ALREADY_EXISTS, "an item with the same name already exists")
		}
	}

	// The folder itself.
	updates := map[string]interface{}{
		"name": name,
		"path": newPath,
	}
	if reparent {
		updates["parent_id"] = targetParentPtr
	}
	if err := tx.Model(&model.SharedFolder{}).Where("id = ?", folder.ID).Updates(updates).Error; err != nil {
		return database.ClassifyError(err)
	}

	// Descendants: uniform prefix substitution keeps the tree consistent.
	for _, sub := range subFolders {
		if sub.ID == folder.ID {
			continue
		}
		if err := tx.Model(&model.SharedFolder{}).Where("id = ?", sub.ID).
			Update("path", newPath+strings.TrimPrefix(sub.Path, oldPrefix)).Error; err != nil {
			return database.ClassifyError(err)
		}
	}
	for _, f := range subFiles {
		if err := tx.Model(&model.SharedFileEntry{}).Where("id = ?", f.ID).
			Update("file_path", newPath+strings.TrimPrefix(f.FilePath, oldPrefix)).Error; err != nil {
			return database.ClassifyError(err)
		}
	}
	return nil
}

// collectSubtree returns the folder itself plus every folder/file row whose
// logical path lives under oldPrefix.
//
// LIKE with an explicit escape character is used for portability: substr/length
// would need an integer length parameter, and PostgreSQL has no implicit
// bigint -> int4 cast for function resolution, so a Go int64 argument can fail
// to match substr(text, int, int).
func collectSubtree(tx *gorm.DB, spaceID, oldPrefix string) ([]model.SharedFolder, []model.SharedFileEntry, error) {
	pattern := escapeLike(oldPrefix) + "/%"

	var subFolders []model.SharedFolder
	if err := tx.Where("space_id = ? AND (path = ? OR path LIKE ? ESCAPE '!')",
		spaceID, oldPrefix, pattern).
		Find(&subFolders).Error; err != nil {
		return nil, nil, err
	}

	var subFiles []model.SharedFileEntry
	if err := tx.Where("space_id = ? AND (file_path = ? OR file_path LIKE ? ESCAPE '!')",
		spaceID, oldPrefix, pattern).
		Find(&subFiles).Error; err != nil {
		return nil, nil, err
	}
	return subFolders, subFiles, nil
}

// escapeLike neutralises LIKE wildcards so a folder literally named "50%" or
// "a_b" cannot widen the subtree query. Pairs with ESCAPE '!'.
func escapeLike(s string) string {
	return strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(s)
}

func destinationPaths(oldPrefix, newPrefix string, subFolders []model.SharedFolder, subFiles []model.SharedFileEntry) []string {
	paths := make([]string, 0, len(subFolders)+len(subFiles))
	for _, f := range subFolders {
		paths = append(paths, newPrefix+strings.TrimPrefix(f.Path, oldPrefix))
	}
	for _, f := range subFiles {
		paths = append(paths, newPrefix+strings.TrimPrefix(f.FilePath, oldPrefix))
	}
	return paths
}

// pathOwner returns the ID of the live folder or file occupying path, or "".
func pathOwner(tx *gorm.DB, spaceID, p string) (string, error) {
	var folder model.SharedFolder
	if err := tx.Select("id").Where("space_id = ? AND path = ?", spaceID, p).
		Limit(1).Find(&folder).Error; err != nil {
		return "", err
	}
	if folder.ID != "" {
		return folder.ID, nil
	}

	var file model.SharedFileEntry
	if err := tx.Select("id").Where("space_id = ? AND file_path = ?", spaceID, p).
		Limit(1).Find(&file).Error; err != nil {
		return "", err
	}
	return file.ID, nil
}

// validateCloudFSName enforces a single path segment within the column limit.
func validateCloudFSName(name string, maxLen int) error {
	if name == "" || name == "." || name == ".." {
		return errors.New(errors.CLOUDFS_NAME_INVALID, "invalid name")
	}
	if strings.ContainsAny(name, "/\\") || strings.ContainsRune(name, 0) {
		return errors.New(errors.CLOUDFS_NAME_INVALID, "name must not contain path separators")
	}
	if utf8.RuneCountInString(name) > maxLen {
		return errors.New(errors.CLOUDFS_NAME_TOO_LONG,
			fmt.Sprintf("name exceeds %d characters", maxLen))
	}
	return nil
}
