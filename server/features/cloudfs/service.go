package cloudfs

import (
	"context"
	"fmt"
	"io"
	"mime"
	"os"
	"path"
	"path/filepath"
	"strings"

	"gorm.io/gorm"

	"ridgericetalk/core/errors"
	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/database"
	gormrepo "ridgericetalk/internal/infra/gorm"
	"ridgericetalk/internal/model"
	"ridgericetalk/internal/repositories"
	"ridgericetalk/internal/storage"
)

// Service handles cloudfs business logic
type Service struct {
	db                  *gorm.DB
	cfg                 *config.Config
	sharedFolderRepo    repositories.SharedFolderRepository
	sharedFileEntryRepo repositories.SharedFileEntryRepository
	// shareTickets 保存密码校验通过后签发的一次性下载凭据（DES-2026-0912-05 §5）。
	// 进程内状态：重启即失效，不会造成任何越权（用户重新校验密码即可）。
	shareTickets *shareTicketStore
}

// NewService creates a new cloudfs service
func NewService(db *gorm.DB, cfg *config.Config) *Service {
	return &Service{
		db:                  db,
		cfg:                 cfg,
		sharedFolderRepo:    gormrepo.NewGormSharedFolderRepository(db),
		sharedFileEntryRepo: gormrepo.NewGormSharedFileEntryRepository(db),
		shareTickets:        newShareTicketStore(),
	}
}

// NewServiceWithRepos creates a new cloudfs service with the given repositories.
func NewServiceWithRepos(db *gorm.DB, cfg *config.Config, sharedFolderRepo repositories.SharedFolderRepository, sharedFileEntryRepo repositories.SharedFileEntryRepository) *Service {
	if sharedFolderRepo == nil {
		sharedFolderRepo = gormrepo.NewGormSharedFolderRepository(db)
	}
	if sharedFileEntryRepo == nil {
		sharedFileEntryRepo = gormrepo.NewGormSharedFileEntryRepository(db)
	}
	return &Service{
		db:                  db,
		cfg:                 cfg,
		sharedFolderRepo:    sharedFolderRepo,
		sharedFileEntryRepo: sharedFileEntryRepo,
		shareTickets:        newShareTicketStore(),
	}
}

// CloudFileItem is the unified response format
//
// Path is only populated by Search, where a hit can live anywhere in the tree
// and the client needs the full location to open it; List already returns the
// directory it was asked about, so it leaves Path empty (omitted from JSON).
type CloudFileItem struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Type      string `json:"type"` // folder or file
	Path      string `json:"path,omitempty"`
	Size      string `json:"size,omitempty"`
	UploadBy  string `json:"uploadBy,omitempty"`
	UpdatedAt string `json:"updatedAt"`
}

func formatSize(bytes int64) string {
	if bytes < 1024 {
		return fmt.Sprintf("%d B", bytes)
	}
	if bytes < 1024*1024 {
		return fmt.Sprintf("%.1f KB", float64(bytes)/1024)
	}
	if bytes < 1024*1024*1024 {
		return fmt.Sprintf("%.1f MB", float64(bytes)/(1024*1024))
	}
	return fmt.Sprintf("%.2f GB", float64(bytes)/(1024*1024*1024))
}

func (s *Service) baseDir() string {
	return filepath.Join(s.cfg.LocalDataPath, "cloudfs")
}

func (s *Service) requireMembership(spaceID, userID string) error {
	if spaceID == "" {
		return errors.New(errors.CLOUDFS_PERMISSION_DENIED, "space not found")
	}
	var count int64
	if err := s.db.Model(&model.Membership{}).Where("space_id = ? AND user_id = ?", spaceID, userID).Count(&count).Error; err != nil {
		return errors.ErrInternal
	}
	if count == 0 {
		return errors.New(errors.CLOUDFS_PERMISSION_DENIED, "no permission to access cloud files")
	}
	return nil
}

func (s *Service) isSpaceAdmin(spaceID, userID string) bool {
	return isSpaceAdminTx(s.db, spaceID, userID)
}

// isSpaceAdminTx is isSpaceAdmin bound to an already-open transaction. Reading
// the membership through the pool while a transaction is open deadlocks when the
// driver caps the pool at a single connection (SQLite test/dev setups), so
// callers inside s.db.Transaction must use this variant.
func isSpaceAdminTx(tx *gorm.DB, spaceID, userID string) bool {
	var membership model.Membership
	if err := tx.Where("space_id = ? AND user_id = ?", spaceID, userID).First(&membership).Error; err != nil {
		return false
	}
	return membership.Role == "OWNER" || membership.Role == "ADMIN"
}

// List returns items at the given path for a given space.
func (s *Service) List(spaceID string, listPath string, userID string) ([]CloudFileItem, string, error) {
	if err := s.requireMembership(spaceID, userID); err != nil {
		return nil, "", err
	}

	// Normalize path
	listPath = path.Clean("/" + listPath)
	if listPath == "." {
		listPath = "/"
	}

	items := make([]CloudFileItem, 0)

	if listPath == "/" {
		// Root path: folders with parent_id IS NULL, files with folder_id = ''
		var folders []model.SharedFolder
		if err := s.db.Where("space_id = ? AND parent_id IS NULL", spaceID).Find(&folders).Error; err != nil {
			return nil, "", errors.ErrInternal
		}
		for _, f := range folders {
			items = append(items, CloudFileItem{
				ID:        f.ID,
				Name:      f.Name,
				Type:      "folder",
				UpdatedAt: f.UpdatedAt.Format("2006-01-02 15:04"),
			})
		}

		var files []model.SharedFileEntry
		if err := s.db.Where("space_id = ? AND folder_id = ''", spaceID).Find(&files).Error; err != nil {
			return nil, "", errors.ErrInternal
		}
		for _, f := range files {
			var uploadBy string
			if f.UploadedBy != "" {
				var u model.User
				s.db.Select("display_name, username").First(&u, "id = ?", f.UploadedBy)
				uploadBy = u.DisplayName
				if uploadBy == "" {
					uploadBy = u.Username
				}
			}
			items = append(items, CloudFileItem{
				ID:        f.ID,
				Name:      f.FileName,
				Type:      "file",
				Size:      formatSize(f.FileSize),
				UploadBy:  uploadBy,
				UpdatedAt: f.CreatedAt.Format("2006-01-02 15:04"),
			})
		}
	} else {
		// Non-root path: find the current folder by path first
		var currentFolder model.SharedFolder
		if err := s.db.Where("space_id = ? AND path = ?", spaceID, listPath).First(&currentFolder).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return items, listPath, nil
			}
			return nil, "", errors.ErrInternal
		}

		// Find sub-folders whose parent_id equals current folder's ID
		var folders []model.SharedFolder
		if err := s.db.Where("parent_id = ?", currentFolder.ID).Find(&folders).Error; err != nil {
			return nil, "", errors.ErrInternal
		}
		for _, f := range folders {
			items = append(items, CloudFileItem{
				ID:        f.ID,
				Name:      f.Name,
				Type:      "folder",
				UpdatedAt: f.UpdatedAt.Format("2006-01-02 15:04"),
			})
		}

		// Find files whose folder_id equals current folder's ID
		var files []model.SharedFileEntry
		if err := s.db.Where("folder_id = ?", currentFolder.ID).Find(&files).Error; err != nil {
			return nil, "", errors.ErrInternal
		}
		for _, f := range files {
			var uploadBy string
			if f.UploadedBy != "" {
				var u model.User
				s.db.Select("display_name, username").First(&u, "id = ?", f.UploadedBy)
				uploadBy = u.DisplayName
				if uploadBy == "" {
					uploadBy = u.Username
				}
			}
			items = append(items, CloudFileItem{
				ID:        f.ID,
				Name:      f.FileName,
				Type:      "file",
				Size:      formatSize(f.FileSize),
				UploadBy:  uploadBy,
				UpdatedAt: f.CreatedAt.Format("2006-01-02 15:04"),
			})
		}
	}

	return items, listPath, nil
}

// CreateFolder creates a new folder within a space.
func (s *Service) CreateFolder(spaceID string, folderPath string, name string, userID string) error {
	if err := s.requireMembership(spaceID, userID); err != nil {
		return err
	}
	folderPath = path.Clean("/" + folderPath)
	if folderPath == "." {
		folderPath = "/"
	}

	fullPath := path.Join(folderPath, name)

	return s.db.Transaction(func(tx *gorm.DB) error {
		// Find parent folder ID if not root
		var parentID *string
		if folderPath != "/" {
			var parentFolder model.SharedFolder
			if err := tx.Where("space_id = ? AND path = ?", spaceID, folderPath).First(&parentFolder).Error; err != nil {
				if err == gorm.ErrRecordNotFound {
					return errors.ErrBadRequest.WithDetails("parent folder not found")
				}
				return err
			}
			parentID = &parentFolder.ID
		}

		// Prevent duplicates and path traversal
		var existing int64
		if err := tx.Model(&model.SharedFolder{}).Where("space_id = ? AND path = ?", spaceID, fullPath).Count(&existing).Error; err != nil {
			return err
		}
		if existing > 0 {
			return errors.New(errors.CLOUDFS_ALREADY_EXISTS, "folder already exists")
		}

		folder := model.SharedFolder{
			ID:       idgen.GenerateID(idgen.PrefixFile),
			SpaceID:  spaceID,
			Name:     name,
			Path:     fullPath,
			ParentID: parentID,
			OwnerID:  userID,
		}
		return tx.Create(&folder).Error
	})
}

// GetOrCreateFolder gets or creates a folder by its full path within a space.
// Returns the folder ID. This is used internally by other features (e.g. channel file archive).
func (s *Service) GetOrCreateFolder(spaceID string, folderPath string, userID string) (string, error) {
	if err := s.requireMembership(spaceID, userID); err != nil {
		return "", err
	}
	folderPath = path.Clean("/" + folderPath)
	if folderPath == "." {
		folderPath = "/"
	}

	// Root folder is represented by empty string
	if folderPath == "/" {
		return "", nil
	}

	var folder model.SharedFolder
	if err := s.db.Where("space_id = ? AND path = ?", spaceID, folderPath).First(&folder).Error; err == nil {
		return folder.ID, nil
	} else if err != gorm.ErrRecordNotFound {
		return "", errors.ErrInternal
	}

	// Need to create folder and possibly parent folders
	parentPath := path.Dir(folderPath)
	folderName := path.Base(folderPath)
	var parentID *string
	if parentPath != "/" {
		pid, err := s.GetOrCreateFolder(spaceID, parentPath, userID)
		if err != nil {
			return "", err
		}
		parentID = &pid
	}

	folder = model.SharedFolder{
		ID:       idgen.GenerateID(idgen.PrefixFile),
		SpaceID:  spaceID,
		ParentID: parentID,
		Name:     folderName,
		Path:     folderPath,
		OwnerID:  userID,
	}
	if err := s.db.Create(&folder).Error; err != nil {
		return "", database.ClassifyError(err)
	}
	return folder.ID, nil
}

// ArchiveChannelFile uploads a file into the channel archive folder (/Channel Files/<channel_name>)
// and returns the created SharedFileEntry. This is used by the message attachment pipeline.
func (s *Service) ArchiveChannelFile(spaceID string, channelID string, channelName string, filename string, content io.Reader, userID string) (*model.SharedFileEntry, error) {
	if err := s.requireMembership(spaceID, userID); err != nil {
		return nil, err
	}

	folderPath := "/Channel Files/" + channelName
	folderID, err := s.GetOrCreateFolder(spaceID, folderPath, userID)
	if err != nil {
		return nil, err
	}

	filename = filepath.Base(filename)
	if filename == "" || filename == "." || filename == "/" {
		return nil, errors.ErrBadRequest.WithDetails("invalid filename")
	}

	baseDir := s.baseDir()
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return nil, errors.ErrInternal.WithDetails("failed to create storage directory")
	}

	uniqueName := idgen.NextString() + "_" + filename
	tempPath := filepath.Join(baseDir, ".tmp_"+uniqueName)
	finalPath := filepath.Join(baseDir, uniqueName)

	f, err := os.Create(tempPath)
	if err != nil {
		return nil, errors.ErrInternal.WithDetails("failed to create file")
	}

	// Hash while streaming so the SHA-256 is available without a second pass
	// over the file (DES-2026-0912-02 §4.6: integrity only, no dedup).
	sum := storage.NewChecksumWriter()
	size, err := io.Copy(io.MultiWriter(f, sum), content)
	f.Close()
	if err != nil {
		os.Remove(tempPath)
		return nil, errors.ErrInternal.WithDetails("failed to save file")
	}

	if quota := int64(s.cfg.CloudFSQuotaGB) * 1024 * 1024 * 1024; quota > 0 {
		used, _, err := s.GetUsage(spaceID)
		if err != nil {
			os.Remove(tempPath)
			return nil, errors.ErrInternal.WithDetails("failed to check storage quota")
		}
		if used+size > quota {
			os.Remove(tempPath)
			return nil, errors.New(errors.CLOUDFS_QUOTA_EXCEEDED, "upload would exceed space storage quota")
		}
	}

	mimeType := mime.TypeByExtension(filepath.Ext(filename))
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	entry := &model.SharedFileEntry{
		ID:           idgen.GenerateID(idgen.PrefixFile),
		SpaceID:      spaceID,
		FolderID:     folderID,
		FileName:     filename,
		FilePath:     path.Join(folderPath, filename),
		PhysicalName: uniqueName,
		FileSize:     size,
		MimeType:     mimeType,
		FileExt:      filepath.Ext(filename),
		VersionNo:    1,
		UploadedBy:   userID,
	}

	meta := model.FileMetadata{
		ID:       idgen.GenerateID(idgen.PrefixFile),
		UserID:   userID,
		FileName: filename,
		FilePath: finalPath,
		FileSize: size,
		MimeType: mimeType,
		Checksum: sum.Sum(),
	}

	err = s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(entry).Error; err != nil {
			return err
		}
		if err := tx.Create(&meta).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		os.Remove(tempPath)
		return nil, database.ClassifyError(err)
	}

	if err := os.Rename(tempPath, finalPath); err != nil {
		os.Remove(tempPath)
		return nil, errors.ErrInternal.WithDetails("failed to finalize file")
	}

	return entry, nil
}

// Upload saves a file and creates metadata atomically within a space.
func (s *Service) Upload(spaceID string, uploadPath string, filename string, content io.Reader, userID string) error {
	if err := s.requireMembership(spaceID, userID); err != nil {
		return err
	}
	uploadPath = path.Clean("/" + uploadPath)
	if uploadPath == "." {
		uploadPath = "/"
	}

	// Ensure filename is pure basename without directory prefixes
	filename = filepath.Base(filename)
	if filename == "" || filename == "." || filename == "/" {
		return errors.ErrBadRequest.WithDetails("invalid filename")
	}

	baseDir := s.baseDir()
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return errors.ErrInternal.WithDetails("failed to create storage directory")
	}

	// Stream to a temporary file first; only commit the final name after DB success.
	uniqueName := idgen.NextString() + "_" + filename
	tempPath := filepath.Join(baseDir, ".tmp_"+uniqueName)
	finalPath := filepath.Join(baseDir, uniqueName)

	f, err := os.Create(tempPath)
	if err != nil {
		return errors.ErrInternal.WithDetails("failed to create file")
	}

	// Hash while streaming (see ArchiveChannelFile) — integrity only, no dedup.
	sum := storage.NewChecksumWriter()
	size, err := io.Copy(io.MultiWriter(f, sum), content)
	f.Close()
	if err != nil {
		os.Remove(tempPath)
		return errors.ErrInternal.WithDetails("failed to save file")
	}

	// Quota check: reject upload if it would exceed the space-level quota.
	if quota := int64(s.cfg.CloudFSQuotaGB) * 1024 * 1024 * 1024; quota > 0 {
		used, _, err := s.GetUsage(spaceID)
		if err != nil {
			os.Remove(tempPath)
			return errors.ErrInternal.WithDetails("failed to check storage quota")
		}
		if used+size > quota {
			os.Remove(tempPath)
			return errors.New(errors.CLOUDFS_QUOTA_EXCEEDED, "upload would exceed space storage quota")
		}
	}

	mimeType := mime.TypeByExtension(filepath.Ext(filename))
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	// Atomically create DB records and move the file into place.
	err = s.db.Transaction(func(tx *gorm.DB) error {
		// Find folder ID by path and verify write permission.
		var folderID string
		if uploadPath != "/" {
			var folder model.SharedFolder
			if err := tx.Where("space_id = ? AND path = ?", spaceID, uploadPath).First(&folder).Error; err == nil {
				folderID = folder.ID
				if folder.OwnerID != userID && !isSpaceAdminTx(tx, spaceID, userID) {
					return errors.New(errors.CLOUDFS_PERMISSION_DENIED, "no permission to upload to this folder")
				}
			} else if err != gorm.ErrRecordNotFound {
				return err
			}
			// If folder not found, file goes to root (folderID = "")
		}

		entry := model.SharedFileEntry{
			ID:           idgen.GenerateID(idgen.PrefixFile),
			SpaceID:      spaceID,
			FolderID:     folderID,
			FileName:     filename,
			FilePath:     path.Join(uploadPath, filename),
			PhysicalName: uniqueName,
			FileSize:     size,
			MimeType:     mimeType,
			FileExt:      filepath.Ext(filename), // L16: 扩展名
			VersionNo:    1,                      // L16: 版本号默认 1
			UploadedBy:   userID,
		}
		if err := tx.Create(&entry).Error; err != nil {
			return err
		}

		// Also write the authoritative file metadata used by admin/quota.
		meta := model.FileMetadata{
			ID:       idgen.GenerateID(idgen.PrefixFile),
			UserID:   userID,
			FileName: filename,
			FilePath: finalPath,
			FileSize: size,
			MimeType: mimeType,
			Checksum: sum.Sum(),
		}
		if err := tx.Create(&meta).Error; err != nil {
			return err
		}

		return nil
	})

	if err != nil {
		os.Remove(tempPath)
		return database.ClassifyError(err)
	}

	if err := os.Rename(tempPath, finalPath); err != nil {
		os.Remove(tempPath)
		return errors.ErrInternal.WithDetails("failed to finalize file")
	}
	return nil
}

// DownloadInfo is the resolved location of a stored file plus its integrity
// metadata. Checksum is the recorded SHA-256 ("" for files uploaded before
// checksums were recorded).
type DownloadInfo struct {
	PhysicalPath string
	FileName     string
	MimeType     string
	Checksum     string
}

// Download returns file content path scoped to a space.
//
// Kept as a value-tuple wrapper because internal/message's CloudFSArchiver
// interface consumes this exact signature (message attachment downloads).
func (s *Service) Download(spaceID string, filePath string, userID string) (string, string, string, error) {
	info, err := s.DownloadInfo(spaceID, filePath, userID)
	if err != nil {
		return "", "", "", err
	}
	return info.PhysicalPath, info.FileName, info.MimeType, nil
}

// DownloadInfo resolves a stored file's physical path, name, MIME type and
// stored SHA-256 for a space member. The checksum is exposed to clients so a
// download can be verified (DES-2026-0912-02 §4.6: integrity only, no dedup).
func (s *Service) DownloadInfo(spaceID string, filePath string, userID string) (*DownloadInfo, error) {
	if err := s.requireMembership(spaceID, userID); err != nil {
		return nil, err
	}

	filePath = path.Clean("/" + filePath)

	entry, err := s.sharedFileEntryRepo.GetByFilePath(context.Background(), spaceID, filePath)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errors.New(errors.CLOUDFS_FILE_NOT_FOUND, "file not found")
		}
		return nil, errors.ErrInternal
	}

	baseDir := s.baseDir()
	// Use PhysicalName directly to locate the file (more reliable than directory scanning)
	if entry.PhysicalName == "" {
		// Fallback for old entries without PhysicalName: scan directory by ID prefix
		entries, err := os.ReadDir(baseDir)
		if err != nil {
			return nil, errors.ErrInternal
		}
		for _, e := range entries {
			if e.IsDir() {
				// .trash and other bookkeeping directories never hold content.
				continue
			}
			if strings.HasPrefix(e.Name(), entry.ID+"_") {
				physicalPath := filepath.Join(baseDir, e.Name())
				// Security: ensure resolved path stays within baseDir
				if !strings.HasPrefix(filepath.Clean(physicalPath), filepath.Clean(baseDir)+string(filepath.Separator)) {
					return nil, errors.New(errors.CLOUDFS_PERMISSION_DENIED, "path traversal detected")
				}
				return &DownloadInfo{
					PhysicalPath: physicalPath,
					FileName:     entry.FileName,
					MimeType:     entry.MimeType,
					Checksum:     s.checksumForPhysicalPath(physicalPath),
				}, nil
			}
		}
		return nil, errors.New(errors.CLOUDFS_FILE_NOT_FOUND, "file not found")
	}

	physicalPath := filepath.Join(baseDir, entry.PhysicalName)
	// Security: ensure resolved path stays within baseDir
	if !strings.HasPrefix(filepath.Clean(physicalPath), filepath.Clean(baseDir)+string(filepath.Separator)) {
		return nil, errors.New(errors.CLOUDFS_PERMISSION_DENIED, "path traversal detected")
	}
	return &DownloadInfo{
		PhysicalPath: physicalPath,
		FileName:     entry.FileName,
		MimeType:     entry.MimeType,
		Checksum:     s.checksumForPhysicalPath(physicalPath),
	}, nil
}

// checksumForPhysicalPath returns the recorded SHA-256 for a stored file, or ""
// when the row is missing or predates checksum recording. Best effort: a lookup
// failure must never break a download.
func (s *Service) checksumForPhysicalPath(physicalPath string) string {
	var meta model.FileMetadata
	if err := s.db.Select("checksum").Where("file_path = ?", physicalPath).
		Limit(1).Find(&meta).Error; err != nil {
		return ""
	}
	return meta.Checksum
}

// Delete removes a file or folder scoped to a space (checks ownership).
func (s *Service) Delete(spaceID string, itemPath string, userID string) error {
	if err := s.requireMembership(spaceID, userID); err != nil {
		return err
	}

	itemPath = path.Clean("/" + itemPath)

	// Try file first
	if entry, err := s.sharedFileEntryRepo.GetByFilePath(context.Background(), spaceID, itemPath); err == nil {
		if entry.UploadedBy != userID && !s.isSpaceAdmin(spaceID, userID) {
			return errors.ErrForbidden
		}
		return s.deleteFileEntry(entry)
	}

	// Try folder
	if folder, err := s.sharedFolderRepo.GetByPath(context.Background(), spaceID, itemPath); err == nil {
		if folder.OwnerID != userID && !s.isSpaceAdmin(spaceID, userID) {
			return errors.ErrForbidden
		}
		return s.deleteFolder(folder.ID)
	}

	return errors.ErrNotFound
}

// deleteFileEntry soft-deletes a file record and parks its content in the trash
// directory (DES-2026-0912-02 §4.3).
//
// The row survives with deleted_at set — exactly the state List/GetUsage already
// filter on — so the trash listing can show it and restore can revive it. The
// FileMetadata row is intentionally NOT deleted: it carries the checksum and
// keeps the admin storage view accounting for bytes that are still on disk
// until the retention window expires and the purge job removes both.
func (s *Service) deleteFileEntry(entry *model.SharedFileEntry) error {
	if err := s.db.Where("id = ?", entry.ID).Delete(&model.SharedFileEntry{}).Error; err != nil {
		return database.ClassifyError(err)
	}
	// Row first, bytes second: if the rename fails the content simply stays at
	// its original path, which restore/purge both handle.
	_ = s.moveToTrash(entry.PhysicalName)
	return nil
}

// deleteFolder recursively soft-deletes a folder and all its children and parks
// every file's content in the trash directory.
//
// The DB work is one transaction; the physical moves happen only after it
// commits, so a rolled-back delete can never leave content stranded in .trash
// while its row is still live.
func (s *Service) deleteFolder(folderID string) error {
	var physicalNames []string
	err := s.db.Transaction(func(tx *gorm.DB) error {
		return s.softDeleteFolderTx(tx, folderID, &physicalNames)
	})
	if err != nil {
		return err
	}
	for _, name := range physicalNames {
		_ = s.moveToTrash(name)
	}
	return nil
}

// softDeleteFolderTx soft-deletes the folder subtree, collecting the physical
// names whose content must follow into the trash.
func (s *Service) softDeleteFolderTx(tx *gorm.DB, folderID string, physicalNames *[]string) error {
	var subFolders []model.SharedFolder
	if err := tx.Where("parent_id = ?", folderID).Find(&subFolders).Error; err != nil {
		return err
	}
	for i := range subFolders {
		if err := s.softDeleteFolderTx(tx, subFolders[i].ID, physicalNames); err != nil {
			return err
		}
	}

	var files []model.SharedFileEntry
	if err := tx.Where("folder_id = ?", folderID).Find(&files).Error; err != nil {
		return err
	}
	for i := range files {
		if err := tx.Where("id = ?", files[i].ID).Delete(&model.SharedFileEntry{}).Error; err != nil {
			return err
		}
		*physicalNames = append(*physicalNames, files[i].PhysicalName)
	}

	return tx.Where("id = ?", folderID).Delete(&model.SharedFolder{}).Error
}

// GetUsage returns total storage usage in bytes for a given space.
func (s *Service) GetUsage(spaceID string) (int64, int64, error) {
	var totalSize int64
	entries, err := s.sharedFileEntryRepo.GetBySpaceID(context.Background(), spaceID)
	if err != nil {
		return 0, 0, database.ClassifyError(err)
	}
	for _, e := range entries {
		totalSize += e.FileSize
	}
	quota := int64(s.cfg.CloudFSQuotaGB) * 1024 * 1024 * 1024
	return totalSize, quota, nil
}
