package whiteboard

import (
	"context"
	"time"

	"gorm.io/gorm"

	"ridgericetalk/core/errors"
	"ridgericetalk/core/idgen"
	gormrepo "ridgericetalk/internal/infra/gorm"
	"ridgericetalk/internal/model"
	"ridgericetalk/internal/repositories"
)

// Service provides whiteboard business logic.
type Service struct {
	db             *gorm.DB
	whiteboardRepo repositories.WhiteboardRepository
}

// NewService creates a new whiteboard service.
func NewService(db *gorm.DB) *Service {
	return &Service{db: db, whiteboardRepo: gormrepo.NewGormWhiteboardRepository(db)}
}

// NewServiceWithRepos creates a new whiteboard service with the given repository.
func NewServiceWithRepos(db *gorm.DB, whiteboardRepo repositories.WhiteboardRepository) *Service {
	if whiteboardRepo == nil {
		whiteboardRepo = gormrepo.NewGormWhiteboardRepository(db)
	}
	return &Service{db: db, whiteboardRepo: whiteboardRepo}
}

// List returns whiteboards for a space, optionally including archived ones.
func (s *Service) List(spaceID string, includeArchived bool) ([]model.Whiteboard, error) {
	list, err := s.whiteboardRepo.ListBySpaceID(context.Background(), spaceID, includeArchived)
	if err != nil {
		return nil, errors.ErrInternal
	}
	return list, nil
}

// Create creates a new whiteboard in the space.
func (s *Service) Create(spaceID, name, createdBy string) (*model.Whiteboard, error) {
	if name == "" {
		return nil, errors.ErrBadRequest.WithDetails("name is required")
	}
	wb := &model.Whiteboard{
		ID:        idgen.GenerateID(idgen.PrefixWhiteboard),
		SpaceID:   spaceID,
		Name:      name,
		Archived:  false,
		CreatedBy: createdBy,
	}
	if err := s.whiteboardRepo.Create(context.Background(), wb); err != nil {
		return nil, errors.ErrInternal
	}
	return wb, nil
}

// Get returns a whiteboard by ID.
func (s *Service) Get(id string) (*model.Whiteboard, error) {
	wb, err := s.whiteboardRepo.GetByID(context.Background(), id)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errors.New(errors.WHITEBOARD_NOT_FOUND, "whiteboard not found")
		}
		return nil, errors.ErrInternal
	}
	return wb, nil
}

// Update renames or archives/unarchives a whiteboard.
func (s *Service) Update(id, userID, role string, name *string, archived *bool) (*model.Whiteboard, error) {
	wb, err := s.Get(id)
	if err != nil {
		return nil, err
	}
	updates := map[string]interface{}{}
	if name != nil {
		updates["name"] = *name
	}
	if archived != nil {
		// Only owner/admin or the creator can archive/unarchive.
		if role != "OWNER" && role != "ADMIN" && wb.CreatedBy != userID {
			return nil, errors.New(errors.WHITEBOARD_ARCHIVE_NOT_ALLOWED, "no permission to archive this whiteboard")
		}
		updates["archived"] = *archived
	}
	if len(updates) == 0 {
		return wb, nil
	}
	if err := s.db.Model(wb).Updates(updates).Error; err != nil {
		return nil, errors.ErrInternal
	}
	return wb, nil
}

// Delete removes a whiteboard and all its strokes.
func (s *Service) Delete(id, role string) error {
	if role != "OWNER" && role != "ADMIN" {
		return errors.New(errors.WHITEBOARD_DELETE_NOT_ALLOWED, "no permission to delete whiteboard")
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("whiteboard_id = ?", id).Delete(&model.WhiteboardStroke{}).Error; err != nil {
			return err
		}
		return tx.Where("id = ?", id).Delete(&model.Whiteboard{}).Error
	})
}

// GetStrokes returns decrypted strokes for a whiteboard.
func (s *Service) GetStrokes(whiteboardID string, decrypt func(string, string) (string, error), defaultKey, legacyKey string) ([]model.WhiteboardStroke, error) {
	strokes, err := s.whiteboardRepo.ListStrokesByWhiteboardID(context.Background(), whiteboardID)
	if err != nil {
		return nil, errors.ErrInternal
	}
	for i := range strokes {
		if strokes[i].Encrypted && strokes[i].Data != "" {
			key := defaultKey
			if strokes[i].EncryptionKeyID == "jwt_secret" {
				key = legacyKey
			}
			decrypted, err := decrypt(strokes[i].Data, key)
			if err != nil {
				strokes[i].Data = ""
				continue
			}
			strokes[i].Data = decrypted
		}
	}
	// username 已退役：作者名读时按 UserID 批量解析当前 displayName（覆盖遗留快照），
	// 与消息模块"改名联动"一致。
	s.resolveStrokeAuthorNames(strokes)
	return strokes, nil
}

// resolveStrokeAuthorNames 按 UserID 批量查询用户显示名并覆盖笔迹的 AuthorName 快照。
// username 不再承担功能用途，历史笔迹始终显示作者当前 displayName。
func (s *Service) resolveStrokeAuthorNames(strokes []model.WhiteboardStroke) {
	if len(strokes) == 0 {
		return
	}
	userIDs := make([]string, 0, len(strokes))
	seen := make(map[string]struct{})
	for _, st := range strokes {
		if st.UserID == "" {
			continue
		}
		if _, ok := seen[st.UserID]; ok {
			continue
		}
		seen[st.UserID] = struct{}{}
		userIDs = append(userIDs, st.UserID)
	}
	if len(userIDs) == 0 {
		return
	}
	var users []model.User
	if err := s.db.Select("id, display_name, username").Where("id IN ?", userIDs).Find(&users).Error; err != nil {
		return
	}
	nameMap := make(map[string]string, len(users))
	for _, u := range users {
		nameMap[u.ID] = u.DisplayName
		if nameMap[u.ID] == "" {
			nameMap[u.ID] = u.Username
		}
	}
	for i := range strokes {
		if n, ok := nameMap[strokes[i].UserID]; ok {
			strokes[i].AuthorName = n
		}
	}
}

// CreateStroke persists a stroke and returns the created record with decrypted data.
func (s *Service) CreateStroke(whiteboardID, userID, authorName, tool, data, color string, width float64, encrypt func(string, string) (string, error), key string) (*model.WhiteboardStroke, error) {
	stroke := &model.WhiteboardStroke{
		ID:           idgen.GenerateID(idgen.PrefixStroke),
		WhiteboardID: whiteboardID,
		UserID:       userID,
		AuthorName:   authorName,
		Tool:         tool,
		Type:         tool, // legacy field
		Color:        color,
		Width:        width,
	}
	if data != "" {
		encrypted, err := encrypt(data, key)
		if err != nil {
			return nil, errors.ErrInternal.WithDetails("failed to encrypt stroke data")
		}
		stroke.Data = encrypted
		stroke.Encrypted = true
		stroke.EncryptionKeyID = "encryption_key_v1"
	}
	if err := s.whiteboardRepo.CreateStroke(context.Background(), stroke); err != nil {
		return nil, errors.ErrInternal
	}
	// Update whiteboard stats in the background; failures are non-fatal.
	go func(wbID string) {
		count, err := s.whiteboardRepo.CountStrokesByWhiteboardID(context.Background(), wbID)
		if err != nil {
			return
		}
		now := time.Now()
		_ = s.whiteboardRepo.UpdateStats(context.Background(), wbID, count, now)
	}(whiteboardID)
	// Return decrypted data for response consistency.
	stroke.Data = data
	stroke.Encrypted = false
	stroke.EncryptionKeyID = ""
	return stroke, nil
}

// ClearStrokes deletes all strokes in a whiteboard.
func (s *Service) ClearStrokes(whiteboardID string) error {
	if err := s.whiteboardRepo.DeleteStrokesByWhiteboardID(context.Background(), whiteboardID); err != nil {
		return err
	}
	// Reset stats in the background.
	go func(wbID string) {
		_ = s.whiteboardRepo.UpdateStats(context.Background(), wbID, 0, time.Now())
	}(whiteboardID)
	return nil
}

// DeleteStroke 删除白板中的单条笔迹（S-3 选中删除）。
// 笔迹不存在时返回 WHITEBOARD_NOT_FOUND（映射 404），由 handler 决定是否照搬给客户端。
// 删除后异步刷新白板统计，与 CreateStroke 的后台统计刷新保持一致。
func (s *Service) DeleteStroke(whiteboardID, strokeID string) error {
	affected, err := s.whiteboardRepo.DeleteStrokeByID(context.Background(), whiteboardID, strokeID)
	if err != nil {
		return errors.ErrInternal
	}
	if affected == 0 {
		return errors.New(errors.WHITEBOARD_NOT_FOUND, "stroke not found")
	}
	go func(wbID string) {
		count, err := s.whiteboardRepo.CountStrokesByWhiteboardID(context.Background(), wbID)
		if err != nil {
			return
		}
		_ = s.whiteboardRepo.UpdateStats(context.Background(), wbID, count, time.Now())
	}(whiteboardID)
	return nil
}

// UpdateThumbnail updates the whiteboard thumbnail URL.
func (s *Service) UpdateThumbnail(whiteboardID string, thumbnailURL string) error {
	return s.whiteboardRepo.UpdateThumbnail(context.Background(), whiteboardID, thumbnailURL)
}

// CanManage returns true if the user can manage (archive/delete) the whiteboard.
func (s *Service) CanManage(wb *model.Whiteboard, userID, role string) bool {
	return role == "OWNER" || role == "ADMIN" || wb.CreatedBy == userID
}
