package message

import (
	"bytes"
	"context"
	"database/sql/driver"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"mime/multipart"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/disintegration/imaging"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"ridgericetalk/core/errors"
	"ridgericetalk/core/idgen"
	"ridgericetalk/core/validator"
	"ridgericetalk/internal/database"
	gormrepo "ridgericetalk/internal/infra/gorm"
	"ridgericetalk/internal/model"
	"ridgericetalk/internal/repositories"
	"ridgericetalk/internal/storage"
)

// CloudFSArchiver abstracts the cloudfs operations needed by message attachments.
type CloudFSArchiver interface {
	ArchiveChannelFile(spaceID string, channelID string, channelName string, filename string, content io.Reader, userID string) (*model.SharedFileEntry, error)
	Download(spaceID string, filePath string, userID string) (string, string, string, error)
}

// Service handles message business logic.
type Service struct {
	db            *gorm.DB
	messageRepo   repositories.MessageRepository
	archiver      CloudFSArchiver
	userCache     *userSnapshotCache
	localDataPath string
}

// userSnapshotCache caches user profile snapshots used when creating messages.
// It reduces per-message DB round-trips under high concurrency while still
// recording accurate author snapshots (unlike using middleware-provided
// username alone).
type userSnapshotCache struct {
	mu      sync.RWMutex
	entries map[string]*userSnapshotEntry
	ttl     time.Duration
}

type userSnapshotEntry struct {
	snapshot  model.User
	expiresAt time.Time
}

func newUserSnapshotCache(ttl time.Duration) *userSnapshotCache {
	return &userSnapshotCache{
		entries: make(map[string]*userSnapshotEntry),
		ttl:     ttl,
	}
}

func (c *userSnapshotCache) get(userID string) (model.User, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry, ok := c.entries[userID]
	if !ok || time.Now().After(entry.expiresAt) {
		return model.User{}, false
	}
	return entry.snapshot, true
}

func (c *userSnapshotCache) set(userID string, snapshot model.User) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[userID] = &userSnapshotEntry{
		snapshot:  snapshot,
		expiresAt: time.Now().Add(c.ttl),
	}
}

// NewService creates a new message service with a default GORM-backed
// MessageRepository (no cache).
func NewService(db *gorm.DB) *Service {
	return &Service{
		db:          db,
		messageRepo: gormrepo.NewGormMessageRepository(db),
		userCache:   newUserSnapshotCache(5 * time.Second),
	}
}

// NewServiceWithRepos creates a new message service with the given
// MessageRepository and optional cloudfs archiver.
func NewServiceWithRepos(db *gorm.DB, messageRepo repositories.MessageRepository) *Service {
	if messageRepo == nil {
		messageRepo = gormrepo.NewGormMessageRepository(db)
	}
	return &Service{db: db, messageRepo: messageRepo, userCache: newUserSnapshotCache(5 * time.Second)}
}

// SetArchiver injects the cloudfs archiver used for message attachments.
func (s *Service) SetArchiver(archiver CloudFSArchiver) {
	s.archiver = archiver
}

// SetLocalDataPath sets the root directory for local file storage.
// It is used when resolving attachment physical paths and thumbnails.
func (s *Service) SetLocalDataPath(path string) {
	if path == "" {
		path = "./storage"
	}
	s.localDataPath = path
}

// GetMessages retrieves messages for a channel with cursor pagination
// Usernames and avatars are dynamically resolved from the current user profile
func (s *Service) GetMessages(channelID string, cursor string, limit int) ([]model.Message, bool, error) {
	if limit < 1 || limit > 100 {
		limit = 50
	}

	query := s.db.Where("channel_id = ? AND parent_id IS NULL", channelID).Order("created_at DESC")
	if cursor != "" {
		var cursorMsg model.Message
		if err := s.db.First(&cursorMsg, "id = ?", cursor).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return nil, false, errors.ErrBadRequest.WithDetails("invalid cursor")
			}
			return nil, false, errors.ErrInternal
		}
		query = query.Where("created_at < ?", cursorMsg.CreatedAt)
	}

	var messages []model.Message
	if err := query.Limit(limit + 1).Find(&messages).Error; err != nil {
		return nil, false, errors.ErrInternal
	}

	hasMore := len(messages) > limit
	if hasMore {
		messages = messages[:limit]
	}

	// Reverse to chronological order
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}

	s.enrichMessages(messages)

	return messages, hasMore, nil
}

// enrichMessages fills per-message enrichment for a batch of messages: dynamic
// author snapshots (username/display name/avatar/role resolved from the CURRENT
// user profile) and attachments. Order of the input slice is irrelevant; both
// passes are keyed lookups. Shared by the list and the around(邻域页) paths.
func (s *Service) enrichMessages(messages []model.Message) {
	// Collect unique user IDs for batch lookup
	userIDs := make(map[string]struct{})
	for _, m := range messages {
		userIDs[m.UserID] = struct{}{}
	}

	// Batch query current user profiles
	if len(userIDs) > 0 {
		uids := make([]string, 0, len(userIDs))
		for id := range userIDs {
			uids = append(uids, id)
		}
		var users []model.User
		if err := s.db.Select("id, username, display_name, avatar, role").Where("id IN ?", uids).Find(&users).Error; err == nil {
			userMap := make(map[string]*model.User)
			for i := range users {
				userMap[users[i].ID] = &users[i]
			}
			// Resolve dynamic username/avatar. AuthorDisplayName is ALWAYS overwritten
			// with the user's CURRENT display name so that renaming a user updates
			// every historical message (Discord/Slack behavior — messages are owned by
			// the user ID, not a frozen name). Empty snapshots (legacy rows) are also
			// filled for backward compatibility.
			for i := range messages {
				if u, ok := userMap[messages[i].UserID]; ok {
					if u.DisplayName != "" {
						messages[i].AuthorDisplayName = u.DisplayName
					} else {
						messages[i].AuthorDisplayName = u.Username
					}
					messages[i].AuthorUsername = u.Username
					if messages[i].AuthorAvatarURL == "" {
						messages[i].AuthorAvatarURL = u.Avatar
					}
					if messages[i].AuthorRole == "" {
						messages[i].AuthorRole = u.Role
					}
				}
			}
		}
	}

	// Load attachments in batch
	if len(messages) > 0 {
		msgIDs := make([]string, len(messages))
		for i, m := range messages {
			msgIDs[i] = m.ID
		}
		var allAttachments []model.MessageAttachment
		if err := s.db.Where("message_id IN ?", msgIDs).Find(&allAttachments).Error; err == nil {
			attMap := make(map[string][]model.MessageAttachment)
			for _, a := range allAttachments {
				attMap[a.MessageID] = append(attMap[a.MessageID], a)
			}
			for i := range messages {
				messages[i].Attachments = attMap[messages[i].ID]
			}
		}
	}
}

// GetMessagesAround returns one page of messages centred on the given message
// (inclusive): floor((limit-1)/2) older messages before the target and the
// rest after it; when one side runs out near the channel boundary the other
// side is extended so the page holds up to limit messages (Discord-style
// around). Output is chronological (same ordering contract as GetMessages).
// hasMore keeps the list semantics: true when older messages exist before
// this page (client keeps paginating backwards with items[0].id as cursor).
// The target must exist AND belong to channelID, otherwise MESSAGE_NOT_FOUND
// (404) is returned.
// AroundInfo 是 around（邻域页）查询的完整结果（A9 二期）。
// ThreadParentID 非空表示 around 命中的是线程回复，页已改为以父消息为中心；
// FocusMessageID 为客户端最初请求定位的消息 ID（回复本身不在页内）。
type AroundInfo struct {
	Messages       []model.Message
	HasMore        bool
	ThreadParentID string
	FocusMessageID string
}

// GetMessagesAround 保留旧签名（页面消息 + hasMore），内部委托 AroundMessages。
func (s *Service) GetMessagesAround(channelID, aroundID string, limit int) ([]model.Message, bool, error) {
	info, err := s.AroundMessages(channelID, aroundID, limit)
	if err != nil {
		return nil, false, err
	}
	return info.Messages, info.HasMore, nil
}

// AroundMessages returns one page centred on aroundID.
// A9 二期：目标查询本身不过滤线程回复（否则回复无法被定位），但页面邻居
// 一律只取顶层消息（parent_id IS NULL）——线程回复不进主列表。若目标是
// 线程回复，则以父消息为中心重跑取页，并回报 threadParentId/focusMessageId。
func (s *Service) AroundMessages(channelID, aroundID string, limit int) (*AroundInfo, error) {
	if limit < 1 || limit > 100 {
		limit = 50
	}

	var target model.Message
	if err := s.db.First(&target, "id = ? AND channel_id = ?", aroundID, channelID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errors.New(errors.MESSAGE_NOT_FOUND, "message not found")
		}
		return nil, errors.ErrInternal
	}

	focusMessageID := ""
	threadParentID := ""
	if target.ParentID != nil && *target.ParentID != "" {
		var parent model.Message
		if err := s.db.First(&parent, "id = ? AND channel_id = ?", *target.ParentID, channelID).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return nil, errors.New(errors.MESSAGE_NOT_FOUND, "message not found")
			}
			return nil, errors.ErrInternal
		}
		focusMessageID = target.ID
		threadParentID = parent.ID
		target = parent
	}

	// Older neighbours, newest-first; fetch one extra to compute hasMore.
	var before []model.Message
	if err := s.db.Where("channel_id = ? AND parent_id IS NULL AND created_at < ?", channelID, target.CreatedAt).
		Order("created_at DESC").Limit((limit-1)/2 + 1).Find(&before).Error; err != nil {
		return nil, errors.ErrInternal
	}
	hasMore := len(before) > (limit-1)/2
	if hasMore {
		before = before[:(limit-1)/2]
	}

	// Newer neighbours: fill the remainder of the page, extending past the
	// centred budget when fewer older messages exist (boundary clamp).
	afterCount := limit - 1 - len(before)
	var after []model.Message
	if err := s.db.Where("channel_id = ? AND parent_id IS NULL AND created_at > ?", channelID, target.CreatedAt).
		Order("created_at ASC").Limit(afterCount).Find(&after).Error; err != nil {
		return nil, errors.ErrInternal
	}

	messages := make([]model.Message, 0, len(before)+1+len(after))
	for i := len(before) - 1; i >= 0; i-- {
		messages = append(messages, before[i])
	}
	messages = append(messages, target)
	messages = append(messages, after...)

	s.enrichMessages(messages)

	return &AroundInfo{
		Messages:       messages,
		HasMore:        hasMore,
		ThreadParentID: threadParentID,
		FocusMessageID: focusMessageID,
	}, nil
}

// SearchMessages searches messages in a channel by content (case-insensitive).
// Returns messages in reverse chronological order with cursor pagination.
func (s *Service) SearchMessages(channelID, query, cursor string, limit int) ([]model.Message, bool, string, error) {
	if limit < 1 || limit > 100 {
		limit = 50
	}
	if strings.TrimSpace(query) == "" {
		return nil, false, "", errors.ErrBadRequest.WithDetails("search query is required")
	}
	query = strings.TrimSpace(query)

	db := s.db.Where("channel_id = ?", channelID)
	db = db.Where("LOWER(content) LIKE LOWER(?) ESCAPE '\\'", "%"+escapeLikePattern(query)+"%")

	if cursor != "" {
		var cursorMsg model.Message
		if err := s.db.First(&cursorMsg, "id = ?", cursor).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return nil, false, "", errors.ErrBadRequest.WithDetails("invalid cursor")
			}
			return nil, false, "", errors.ErrInternal
		}
		db = db.Where("created_at < ?", cursorMsg.CreatedAt)
	}

	var messages []model.Message
	if err := db.Order("created_at DESC").Limit(limit + 1).Find(&messages).Error; err != nil {
		return nil, false, "", errors.ErrInternal
	}

	hasMore := len(messages) > limit
	nextCursor := ""
	if hasMore {
		messages = messages[:limit]
		nextCursor = messages[limit-1].ID
	}

	// Resolve author snapshot fields if missing.
	userIDs := make(map[string]struct{})
	for i := range messages {
		userIDs[messages[i].UserID] = struct{}{}
	}
	if len(userIDs) > 0 {
		uids := make([]string, 0, len(userIDs))
		for id := range userIDs {
			uids = append(uids, id)
		}
		var users []model.User
		if err := s.db.Select("id, username, display_name, avatar, role").Where("id IN ?", uids).Find(&users).Error; err == nil {
			userMap := make(map[string]*model.User)
			for i := range users {
				userMap[users[i].ID] = &users[i]
			}
			for i := range messages {
				if u, ok := userMap[messages[i].UserID]; ok {
					// Always overwrite AuthorDisplayName with the current display name
					// (renamed users update their historical messages).
					if u.DisplayName != "" {
						messages[i].AuthorDisplayName = u.DisplayName
					} else {
						messages[i].AuthorDisplayName = u.Username
					}
					if messages[i].AuthorUsername == "" {
						messages[i].AuthorUsername = u.Username
					}
					if messages[i].AuthorAvatarURL == "" {
						messages[i].AuthorAvatarURL = u.Avatar
					}
					if messages[i].AuthorRole == "" {
						messages[i].AuthorRole = u.Role
					}
				}
			}
		}
	}

	// Reverse to chronological order for the response.
	for i, j := 0, len(messages)-1; i < j; i, j = i+1, j-1 {
		messages[i], messages[j] = messages[j], messages[i]
	}

	return messages, hasMore, nextCursor, nil
}

// GlobalSearchOptions describes the already-authorized message search scope.
// ChannelIDs must come from the channel visibility check in the HTTP handler.
type GlobalSearchOptions struct {
	Query      string
	ChannelIDs []string
	AuthorID   string
	Before     *time.Time
	After      *time.Time
	Limit      int
}

// GlobalSearchResult is the stable wire representation for global search.
type GlobalSearchResult struct {
	MessageID   string    `json:"message_id"`
	ParentID    *string   `json:"parent_id"` // A9 二期：线程回复时指向父消息（顶层消息为 null）
	ChannelID   string    `json:"channel_id"`
	ChannelName string    `json:"channel_name"`
	AuthorID    string    `json:"author_id"`
	Author      string    `json:"author"`
	Content     string    `json:"content"`
	CreatedAt   time.Time `json:"created_at"`
}

func escapeLikePattern(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `%`, `\%`)
	return strings.ReplaceAll(value, `_`, `\_`)
}

// SearchAllMessages searches the caller's authorized ordinary text channels.
func (s *Service) SearchAllMessages(options GlobalSearchOptions) ([]GlobalSearchResult, int64, error) {
	queryText := strings.TrimSpace(options.Query)
	if queryText == "" || len(options.ChannelIDs) == 0 {
		return []GlobalSearchResult{}, 0, nil
	}
	if options.Limit < 1 || options.Limit > 100 {
		options.Limit = 25
	}

	buildQuery := func() *gorm.DB {
		db := s.db.Table("messages AS m").
			Select(`m.id AS message_id, m.channel_id, c.name AS channel_name,
				m.user_id AS author_id,
				COALESCE(NULLIF(u.display_name, ''), NULLIF(u.username, ''),
					NULLIF(m.author_display_name, ''), m.author_username) AS author,
				m.content, m.created_at, m.parent_id AS parent_id`).
			Joins("JOIN channels AS c ON c.id = m.channel_id").
			Joins("LEFT JOIN users AS u ON u.id = m.user_id").
			Where("m.deleted_at IS NULL AND c.deleted_at IS NULL").
			Where("LOWER(c.type) = ?", "text").
			Where("LOWER(m.content) LIKE LOWER(?) ESCAPE '\\'", "%"+escapeLikePattern(queryText)+"%").
			Where("m.channel_id IN ?", options.ChannelIDs)
		if options.AuthorID != "" {
			db = db.Where("m.user_id = ?", options.AuthorID)
		}
		if options.Before != nil {
			db = db.Where("m.created_at < ?", *options.Before)
		}
		if options.After != nil {
			db = db.Where("m.created_at > ?", *options.After)
		}
		return db
	}

	countQuery := buildQuery()
	var total int64
	if err := countQuery.Count(&total).Error; err != nil {
		return nil, 0, errors.ErrInternal
	}

	var results []GlobalSearchResult
	if err := buildQuery().Order("m.created_at DESC, m.id DESC").Limit(options.Limit).Find(&results).Error; err != nil {
		return nil, 0, errors.ErrInternal
	}
	if results == nil {
		results = []GlobalSearchResult{}
	}
	return results, total, nil
}

// CreateMessage creates a new message with optional file attachments.
// content may be empty when attachments are present. parentID is ignored in this
// simplified design (reply/thread removed).
//
// 消息转发（一期）扩展参数：
//   - forwardFrom：转发来源作者显示名快照。可空；非空时仅做长度校验（≤64 字符），
//     服务端不解析、不与被转发消息联动（无引用、无反向链接），只原样落库展示。
//   - attachmentIDs：转发复制附件的源附件 ID 列表（带附件消息转发时由客户端传入）。
//     实现为「附件引用复制」：为每条源附件新建一条 MessageAttachment 行并指向同一
//     CloudFileID，物理文件零拷贝；源附件不存在/非法 ID 宽松忽略（不阻断转发）。
//     权限模型沿用私有部署语义：调用者能读到源消息（可见即可转发），目标频道的
//     写权限由路由中间件 ChannelPermission 把关。
func (s *Service) CreateMessage(channelID, userID, username, role, content, clientMessageID, parentID, forwardFrom string, files []*multipart.FileHeader, attachmentIDs []string) (*model.Message, error) {
	content = strings.TrimSpace(content)
	forwardFrom = strings.TrimSpace(forwardFrom)
	if content == "" && len(files) == 0 && len(attachmentIDs) == 0 {
		return nil, errors.ErrBadRequest.WithDetails("message content or attachments required")
	}
	if len(files) > 10 {
		return nil, errors.ErrBadRequest.WithDetails("too many attachments (max 10)")
	}
	// 去重去空后的复制附件 ID 清单，上限与上传附件一致（10 个）。
	cleanAttIDs := make([]string, 0, len(attachmentIDs))
	seenAttIDs := make(map[string]struct{}, len(attachmentIDs))
	for _, id := range attachmentIDs {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, dup := seenAttIDs[id]; dup {
			continue
		}
		seenAttIDs[id] = struct{}{}
		cleanAttIDs = append(cleanAttIDs, id)
	}
	if len(cleanAttIDs) > 10 {
		return nil, errors.ErrBadRequest.WithDetails("too many attachments (max 10)")
	}
	if utf8.RuneCountInString(forwardFrom) > maxForwardFromLen {
		return nil, errors.ErrBadRequest.WithDetails(fmt.Sprintf("forwardFrom too long (max %d characters)", maxForwardFromLen))
	}
	if len(content) > 0 {
		if valid, reason := validator.ValidateMessageContent(content); !valid {
			return nil, errors.New(errors.MESSAGE_TOO_LONG, reason)
		}
	}

	// Idempotency: if clientMessageID is provided, check for existing message
	if clientMessageID != "" {
		var existing model.Message
		err := s.db.Where("client_message_id = ? AND channel_id = ?", clientMessageID, channelID).First(&existing).Error
		if err == nil {
			return s.loadMessageAttachments(&existing)
		}
		if err != gorm.ErrRecordNotFound {
			return nil, database.ClassifyError(err)
		}
	}

	// Fetch channel info for archive path (only needed for attachments)
	var chName, spaceID string
	if len(files) > 0 {
		var ch model.Channel
		if err := s.db.First(&ch, "id = ?", channelID).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return nil, errors.New(errors.CHANNEL_NOT_FOUND, "channel not found")
			}
			return nil, errors.ErrInternal
		}
		if strings.ToLower(ch.Type) != "text" {
			return nil, errors.New(errors.CHANNEL_NOT_VOICE, "channel is not a text channel")
		}
		if ch.SpaceID == "" {
			var space model.Space
			if err := s.db.Order("created_at ASC, id ASC").First(&space).Error; err == nil {
				ch.SpaceID = space.ID
			}
		}
		spaceID = ch.SpaceID
		chName = ch.Name
	}

	// Resolve author snapshot. Use a short-lived cache to avoid querying the
	// users table for every message under high concurrency while still recording
	// accurate display name/avatar snapshots.
	displayName := username
	avatarURL := ""
	if cached, ok := s.userCache.get(userID); ok {
		if cached.DisplayName != "" {
			displayName = cached.DisplayName
		} else {
			displayName = cached.Username
		}
		avatarURL = cached.Avatar
	} else {
		var user model.User
		if err := s.db.Select("id, username, display_name, avatar").Where("id = ?", userID).First(&user).Error; err == nil {
			if user.DisplayName != "" {
				displayName = user.DisplayName
			} else {
				displayName = user.Username
			}
			avatarURL = user.Avatar
			s.userCache.set(userID, user)
		}
	}

	msgID := idgen.GenerateID(idgen.PrefixMessage)
	// 转发复制附件：先按源附件 ID 批量加载，再为每条源附件生成新行（新附件 ID、
	// 复用源附件的类型/文件名/尺寸/CloudFileID）。缩略图按附件 ID 独立存储，新行
	// 没有 thumb 文件，DownloadAttachment(?thumb=1) 会自动回退原图，无需额外处理。
	copiedAtts := make([]*model.MessageAttachment, 0, len(cleanAttIDs))
	if len(cleanAttIDs) > 0 {
		var srcs []model.MessageAttachment
		if err := s.db.Where("id IN ?", cleanAttIDs).Find(&srcs).Error; err != nil {
			return nil, errors.ErrInternal
		}
		srcByID := make(map[string]model.MessageAttachment, len(srcs))
		for _, src := range srcs {
			srcByID[src.ID] = src
		}
		for _, id := range cleanAttIDs {
			src, ok := srcByID[id]
			if !ok {
				continue // 源附件不存在（已删除/非法 ID）：宽松忽略，不阻断转发
			}
			newAttID := idgen.GenerateID(idgen.PrefixMessage)
			copiedAtts = append(copiedAtts, &model.MessageAttachment{
				ID:          newAttID,
				MessageID:   msgID,
				Type:        src.Type,
				FileName:    src.FileName,
				FileSize:    src.FileSize,
				MimeType:    src.MimeType,
				FileURL:     fmt.Sprintf("/api/channel-files/%s", newAttID),
				ThumbURL:    fmt.Sprintf("/api/channel-files/%s?thumb=1", newAttID),
				Width:       src.Width,
				Height:      src.Height,
				CloudFileID: src.CloudFileID,
			})
		}
		// 内容为空且一条有效源附件都没命中时，等价于空消息，拒绝创建。
		if content == "" && len(copiedAtts) == 0 {
			return nil, errors.ErrBadRequest.WithDetails("message content or attachments required")
		}
	}

	msgType := s.messageTypeForAttachments(files)
	if msgType == "text" {
		// 无新上传文件、仅复制附件引用时，按源附件类型推导消息类型（image/video/file/mixed）。
		msgType = messageTypeForCopiedAttachments(copiedAtts)
	}
	msg := &model.Message{
		ID:        msgID,
		ChannelID: channelID,
		UserID:    userID,
		// username 已退役：作者快照字段统一存显示名（AuthorUsername 亦为显示名，读时仍按 UserID 解析）
		AuthorUsername:    displayName,
		AuthorDisplayName: displayName,
		AuthorAvatarURL:   avatarURL,
		AuthorRole:        role,
		Content:           content,
		Type:              msgType,
		ForwardFrom:       forwardFrom,
	}
	if parentID = strings.TrimSpace(parentID); parentID != "" {
		// 线程回复：保留与父消息的关联关系（PRD US-F04-3）。父消息存在性校验
		// 保持宽松（不做强制 FK 校验），thread 查询自然按 parent_id 归组。
		msg.ParentID = &parentID
	}
	if clientMessageID != "" {
		msg.ClientMessageID = &clientMessageID
	}

	// Parse @mentions: convert "@显示名" to "<@user_xxx>" (Discord-style ID-bound
	// mentions). Resolution is by display name (or username) against the space's
	// members; exact "<@user_xxx>" tokens (from the client mention picker) pass
	// through untouched. The rewritten content is stored (Discord stores mentions
	// as IDs, so renaming a user updates every historical mention automatically).
	// mentionUserIDs is returned so the handler can push notifications and bump
	// MentionCount.
	content, mentionUserIDs := s.parseMentions(channelID, content)
	msg.Content = content

	// FIX-B2-1(NJ-10)：附件归档必须在 CreateMessage 事务之外执行。
	// ArchiveChannelFile 会经连接池另开连接写 shared_file_entries/file_metadata
	//（GetOrCreateFolder 首次还会写 shared_folders），而本事务已在连接 A 上持有
	// SQLite 写锁（WAL 单写者），事务内的第二次写会自等待 busy_timeout(5s) 后报
	// database is locked → ClassifyError 判为 SYSTEM_SERVICE_UNAVAILABLE(503)，
	// 即「带附件消息必 503」的根因；单连接池（测试环境）下则直接池等待挂死。
	// 因此先在无事务状态下归档全部附件（msg.ID 事务前已生成），事务内只做纯 DB
	// 写；归档或落库失败时尽力回收已归档条目，避免孤儿文件。
	atts := make([]*model.MessageAttachment, 0, len(files))
	for _, fh := range files {
		att, err := s.processAttachment(spaceID, channelID, chName, msg.ID, fh, userID)
		if err != nil {
			s.cleanupOrphanAttachments(atts)
			return nil, err
		}
		atts = append(atts, att)
	}

	if err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(msg).Error; err != nil {
			return err
		}
		for _, att := range atts {
			if err := tx.Create(att).Error; err != nil {
				return err
			}
		}
		// 转发复制的附件引用行（指向既有 CloudFileID）与新消息同事务落库
		for _, att := range copiedAtts {
			if err := tx.Create(att).Error; err != nil {
				return err
			}
		}
		read := &model.UserChannelRead{
			ID:                idgen.GenerateID(idgen.PrefixMessage),
			UserID:            userID,
			ChannelID:         channelID,
			MessageID:         msg.ID,
			LastReadMessageID: msg.ID,
			ReadAt:            time.Now().UTC(),
		}
		if err := tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "user_id"}, {Name: "channel_id"}},
			DoUpdates: clause.AssignmentColumns([]string{"message_id", "last_read_message_id", "read_at"}),
		}).Create(read).Error; err != nil {
			return err
		}
		return nil
	}); err != nil {
		s.cleanupOrphanAttachments(atts)
		return nil, database.ClassifyError(err)
	}

	// Store mention targets for the handler to send notifications.
	if len(mentionUserIDs) > 0 {
		msg.MentionUserIDs = mentionUserIDs
	}

	return s.loadMessageAttachments(msg)
}

// cleanupOrphanAttachments best-effort reclaims attachments that were already
// archived into CloudFS but whose message row ultimately failed to persist:
// removes the physical file, the FileMetadata row and soft-deletes the
// SharedFileEntry. Cleanup failures are ignored so they never mask the
// original error returned to the caller.
func (s *Service) cleanupOrphanAttachments(atts []*model.MessageAttachment) {
	for _, att := range atts {
		if att == nil || att.CloudFileID == "" {
			continue
		}
		var entry model.SharedFileEntry
		if err := s.db.First(&entry, "id = ?", att.CloudFileID).Error; err == nil && entry.PhysicalName != "" {
			var meta model.FileMetadata
			// FileMetadata.FilePath is the physical path (baseDir/PhysicalName)
			// written by ArchiveChannelFile; match on the unique physical name.
			if err := s.db.Where("file_path LIKE ?", "%"+entry.PhysicalName).First(&meta).Error; err == nil {
				if meta.FilePath != "" {
					_ = os.Remove(meta.FilePath)
				}
				_ = s.db.Delete(&meta).Error
			}
		}
		_ = s.db.Delete(&model.SharedFileEntry{}, "id = ?", att.CloudFileID).Error
	}
}

// mentionTokenRe matches an exact Discord-style mention token "<@user_xxx>".
var mentionTokenRe = regexp.MustCompile(`<@([a-zA-Z0-9_]+)>`)

// parseMentions converts "@显示名" inline mentions into "<@user_xxx>" tokens and
// returns the set of mentioned userIDs. It handles two forms:
//   - "<@user_xxx>" (exact, produced by the client mention picker) — validated
//     against the members table and kept as-is.
//   - "@显示名" or "@username" (free-typed) — resolved by display name/username
//     against the channel's space members.
//
// The rewritten content is stored (Discord stores mentions as IDs, so renaming
// a user updates every historical mention automatically).
func (s *Service) parseMentions(channelID, content string) (string, map[string]bool) {
	if !strings.Contains(content, "@") {
		return content, nil
	}

	// Load the channel's space membership to resolve names → IDs.
	var channel model.Channel
	if err := s.db.First(&channel, "id = ?", channelID).Error; err != nil {
		return content, nil
	}
	var members []model.Membership
	if err := s.db.Where("space_id = ?", channel.SpaceID).Find(&members).Error; err != nil {
		return content, nil
	}
	memberIDs := make([]string, 0, len(members))
	for _, m := range members {
		memberIDs = append(memberIDs, m.UserID)
	}
	var users []model.User
	if len(memberIDs) > 0 {
		if err := s.db.Select("id, username, display_name").Where("id IN ?", memberIDs).Find(&users).Error; err != nil {
			return content, nil
		}
	}
	// Build a lookup: displayName → userID and username → userID. Also track the
	// set of known member IDs to validate exact <@user_xxx> tokens.
	byDisplay := make(map[string]string)
	byUsername := make(map[string]string)
	memberIDSet := make(map[string]bool)
	for _, u := range users {
		if u.DisplayName != "" {
			byDisplay[u.DisplayName] = u.ID
		}
		byUsername[u.Username] = u.ID
		memberIDSet[u.ID] = true
	}

	// First pass: validate exact <@user_xxx> tokens and keep only ones that
	// resolve to actual space members.
	validIDs := make(map[string]bool)
	content = mentionTokenRe.ReplaceAllStringFunc(content, func(token string) string {
		m := mentionTokenRe.FindStringSubmatch(token)
		if len(m) < 2 {
			return token
		}
		id := m[1]
		if memberIDSet[id] {
			validIDs[id] = true
			return token
		}
		// Not a known member — keep the token as-is (client may have picked from a
		// stale list); the renderer will show "@未知用户" rather than dropping text.
		return token
	})

	// Second pass: free-typed "@名字" → "<@user_xxx>". Match display name or
	// username; require a following non-name char or end to avoid matching partial.
	// We scan char-by-char to also avoid re-matching inside an existing token.
	mentionUserIDs := make(map[string]bool)
	for id := range validIDs {
		mentionUserIDs[id] = true
	}
	var sb strings.Builder
	for i := 0; i < len(content); {
		if content[i] == '@' && (i == 0 || content[i-1] != '<') {
			// Try to match a member name starting here.
			matchedID := ""
			matchedLen := 0
			for name, id := range byDisplay {
				if strings.HasPrefix(content[i+1:], name) && len(name) > matchedLen {
					// Boundary check: next char after name must not be name-char.
					next := i + 1 + len(name)
					if next >= len(content) || !isMentionNameChar(content[next]) {
						matchedID = id
						matchedLen = len(name)
					}
				}
			}
			for name, id := range byUsername {
				if strings.HasPrefix(content[i+1:], name) && len(name) > matchedLen {
					next := i + 1 + len(name)
					if next >= len(content) || !isMentionNameChar(content[next]) {
						matchedID = id
						matchedLen = len(name)
					}
				}
			}
			if matchedID != "" {
				mentionUserIDs[matchedID] = true
				sb.WriteString("<@" + matchedID + ">")
				i += 1 + matchedLen
				continue
			}
		}
		sb.WriteByte(content[i])
		i++
	}

	return sb.String(), mentionUserIDs
}

// isMentionNameChar reports whether b is a character that can continue a name
// (alphanumeric, underscore, or CJK). Used for mention boundary detection.
func isMentionNameChar(b byte) bool {
	return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' || b >= '0' && b <= '9' || b == '_' || b >= 0x80
}

func (s *Service) messageTypeForAttachments(files []*multipart.FileHeader) string {
	if len(files) == 0 {
		return "text"
	}
	hasImage, hasVideo, hasFile := false, false, false
	for _, f := range files {
		t := classifyAttachmentType(f.Filename)
		switch t {
		case "image":
			hasImage = true
		case "video":
			hasVideo = true
		default:
			hasFile = true
		}
	}
	return combineAttachmentTypes(hasImage, hasVideo, hasFile)
}

// messageTypeForCopiedAttachments 与 messageTypeForAttachments 同族：基于「已存在的
// 附件行类型」（转发复制附件引用场景）推导消息类型，组合规则保持一致。
func messageTypeForCopiedAttachments(atts []*model.MessageAttachment) string {
	if len(atts) == 0 {
		return "text"
	}
	hasImage, hasVideo, hasFile := false, false, false
	for _, a := range atts {
		switch a.Type {
		case "image":
			hasImage = true
		case "video":
			hasVideo = true
		default:
			hasFile = true
		}
	}
	return combineAttachmentTypes(hasImage, hasVideo, hasFile)
}

// combineAttachmentTypes 折算附件类别组合 → 消息 type（单类取单值，混类记 mixed）。
func combineAttachmentTypes(hasImage, hasVideo, hasFile bool) string {
	if hasImage && !hasVideo && !hasFile {
		return "image"
	}
	if hasVideo && !hasImage && !hasFile {
		return "video"
	}
	if hasFile && !hasImage && !hasVideo {
		return "file"
	}
	return "mixed"
}

func classifyAttachmentType(filename string) string {
	ext := strings.ToLower(filepath.Ext(filename))
	switch ext {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".svg", ".bmp",
		".heic", ".heif", ".avif", ".tiff", ".tif", ".ico":
		return "image"
	case ".mp4", ".mov", ".webm", ".mkv", ".avi", ".flv", ".m4v",
		".ts", ".m2ts", ".3gp", ".mpeg", ".mpg":
		return "video"
	default:
		return "file"
	}
}

const (
	maxImageUploadSize = 20 * 1024 * 1024        // 20MB
	maxVideoUploadSize = 2 * 1024 * 1024 * 1024  // 2GB
	maxFileUploadSize  = 10 * 1024 * 1024 * 1024 // 10GB
	chatImageMaxWidth  = 1920
	chatImageMaxHeight = 1920
	thumbMaxWidth      = 400
	thumbQuality       = 80
	compressedQuality  = 85
	// maxForwardFromLen 转发来源显示名快照长度上限（字符数，与 model.Message 的
	// gorm size:64 对齐）。只存显示名快照，不做引用联动。
	maxForwardFromLen = 64
)

// processAttachment validates, archives and describes one uploaded file.
// FIX-B2-1(NJ-10)：必须在外层事务之外调用——内部经 CloudFSArchiver 走连接池
// 另开连接写库，事务内调用会形成 SQLite 写锁自等待（见 CreateMessage 注释）。
// tx 形参历史上即未被使用，已移除。
func (s *Service) processAttachment(spaceID, channelID, channelName, messageID string, fh *multipart.FileHeader, userID string) (*model.MessageAttachment, error) {
	filename := fh.Filename
	attType := classifyAttachmentType(filename)

	var maxSize int64
	switch attType {
	case "image":
		maxSize = maxImageUploadSize
	case "video":
		maxSize = maxVideoUploadSize
	default:
		maxSize = maxFileUploadSize
	}
	if fh.Size > maxSize {
		return nil, errors.New(errors.FILE_TOO_LARGE, fmt.Sprintf("%s exceeds size limit", filename))
	}

	file, err := fh.Open()
	if err != nil {
		return nil, errors.ErrInternal.WithDetails("failed to open uploaded file")
	}
	defer file.Close()

	// Validate file magic numbers
	buf := make([]byte, 512)
	n, _ := file.Read(buf)
	buf = buf[:n]
	reader := io.MultiReader(bytes.NewReader(buf), file)

	expectedTypes := []string{
		"image/jpeg", "image/png", "image/gif", "image/webp", "image/svg+xml",
		"image/avif", "image/heic", "image/heif", "image/tiff", "image/x-icon",
		"video/mp4", "video/webm", "video/quicktime", "video/x-msvideo",
		"video/x-matroska", "video/x-flv", "video/mp2t", "video/3gpp",
		"application/pdf", "application/zip",
	}
	detectedType, valid := storage.ValidateFileType(bytes.NewReader(buf), expectedTypes...)
	if !valid {
		// For unverified types (e.g. .bmp/.mov/.webm/.mkv/.avi whose magic
		// numbers are not in the whitelist, or any generic file), accept as
		// octet-stream. classifyAttachmentType already decided the logical
		// category from the extension; rejecting here would make those
		// extensions unuploadable even though they are explicitly classified.
		// Magic-number mismatches are still logged via detectedType == "".
		detectedType = "application/octet-stream"
	}

	att := &model.MessageAttachment{
		ID:        idgen.GenerateID(idgen.PrefixMessage),
		MessageID: messageID,
		Type:      attType,
		FileName:  filename,
		FileSize:  fh.Size,
		MimeType:  detectedType,
	}

	var archiveReader io.Reader = reader
	var thumbPath string

	if attType == "image" {
		// Read entire image into memory for processing (20MB image limit makes this safe)
		data, err := io.ReadAll(reader)
		if err != nil {
			return nil, errors.ErrInternal.WithDetails("failed to read image")
		}
		compressed, thumb, width, height, err := s.processImage(bytes.NewReader(data), filename)
		if err == nil && compressed != nil {
			archiveReader = compressed
			att.Width = width
			att.Height = height
		} else {
			// Fallback: archive original
			archiveReader = bytes.NewReader(data)
		}
		if thumb != nil {
			thumbPath = s.saveThumb(thumb, att.ID)
			if thumbPath != "" {
				att.ThumbURL = fmt.Sprintf("/api/channel-files/%s?thumb=1", att.ID)
			}
		}
	}

	if s.archiver == nil {
		return nil, errors.ErrInternal.WithDetails("cloudfs archiver not configured")
	}

	entry, err := s.archiver.ArchiveChannelFile(spaceID, channelID, channelName, filename, archiveReader, userID)
	if err != nil {
		return nil, err
	}
	att.CloudFileID = entry.ID
	att.FileURL = fmt.Sprintf("/api/channel-files/%s", att.ID)

	return att, nil
}

func (s *Service) processImage(r io.Reader, filename string) (io.Reader, image.Image, int, int, error) {
	src, format, err := image.Decode(r)
	if err != nil {
		return nil, nil, 0, 0, err
	}
	bounds := src.Bounds()
	width, height := bounds.Dx(), bounds.Dy()

	// Resize if too large
	resized := src
	if width > chatImageMaxWidth || height > chatImageMaxHeight {
		resized = imaging.Fit(src, chatImageMaxWidth, chatImageMaxHeight, imaging.Lanczos)
		bounds = resized.Bounds()
		width, height = bounds.Dx(), bounds.Dy()
	}

	// Generate thumbnail
	thumb := imaging.Fit(src, thumbMaxWidth, thumbMaxWidth, imaging.Lanczos)

	// For PNG with transparency, keep PNG; otherwise compress to JPEG
	var compressed io.Reader
	ext := strings.ToLower(filepath.Ext(filename))
	if format == "png" || ext == ".png" {
		var b bytes.Buffer
		if err := png.Encode(&b, resized); err != nil {
			return nil, nil, 0, 0, err
		}
		compressed = &b
	} else {
		var b bytes.Buffer
		if err := jpeg.Encode(&b, resized, &jpeg.Options{Quality: compressedQuality}); err != nil {
			return nil, nil, 0, 0, err
		}
		compressed = &b
	}

	return compressed, thumb, width, height, nil
}

func (s *Service) saveThumb(thumb image.Image, attachmentID string) string {
	if thumb == nil {
		return ""
	}
	var b bytes.Buffer
	if err := jpeg.Encode(&b, thumb, &jpeg.Options{Quality: thumbQuality}); err != nil {
		return ""
	}
	// Use cloudfs base dir for thumbs; actual serving uses attachment download endpoint
	thumbDir := filepath.Join("./storage", "channel-thumbs")
	_ = os.MkdirAll(thumbDir, 0755)
	thumbPath := filepath.Join(thumbDir, attachmentID+"_thumb.jpg")
	if err := os.WriteFile(thumbPath, b.Bytes(), 0644); err != nil {
		return ""
	}
	return thumbPath
}

// DownloadAttachment returns the physical path, filename and mime type for an attachment.
// thumb=true returns the thumbnail file if available.
func (s *Service) DownloadAttachment(attachmentID string, userID string, thumb bool) (string, string, string, error) {
	var att model.MessageAttachment
	if err := s.db.First(&att, "id = ?", attachmentID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return "", "", "", errors.New(errors.FILE_NOT_FOUND, "attachment not found")
		}
		return "", "", "", errors.ErrInternal
	}

	// Authorization: check user belongs to the channel's space.
	// Resolve via message → channel → space (att.MessageID is a message ID,
	// not a channel ID, so we must look up the message first).
	var msg model.Message
	if err := s.db.Select("channel_id").First(&msg, "id = ?", att.MessageID).Error; err != nil {
		return "", "", "", errors.ErrForbidden
	}
	var ch model.Channel
	if err := s.db.Select("space_id").First(&ch, "id = ?", msg.ChannelID).Error; err != nil {
		return "", "", "", errors.ErrForbidden
	}

	var count int64
	if err := s.db.Model(&model.Membership{}).Where("space_id = ? AND user_id = ?", ch.SpaceID, userID).Count(&count).Error; err != nil {
		return "", "", "", errors.ErrInternal
	}
	if count == 0 {
		return "", "", "", errors.ErrForbidden
	}

	if s.localDataPath == "" {
		s.localDataPath = "./storage"
	}

	if thumb {
		thumbPath := filepath.Join(s.localDataPath, "channel-thumbs", attachmentID+"_thumb.jpg")
		if _, err := os.Stat(thumbPath); err == nil {
			return thumbPath, att.FileName, "image/jpeg", nil
		}
		// Fallback to original
	}

	if s.archiver == nil {
		return "", "", "", errors.ErrInternal.WithDetails("archiver not configured")
	}
	if att.CloudFileID == "" {
		return "", "", "", errors.New(errors.FILE_NOT_FOUND, "attachment file not found")
	}
	var entry model.SharedFileEntry
	if err := s.db.First(&entry, "id = ?", att.CloudFileID).Error; err != nil {
		return "", "", "", errors.New(errors.FILE_NOT_FOUND, "attachment file not found")
	}
	baseDir := filepath.Join(s.localDataPath, "cloudfs")
	physicalPath := filepath.Join(baseDir, entry.PhysicalName)
	// Path traversal check
	absBase, _ := filepath.Abs(baseDir)
	absPath, _ := filepath.Abs(physicalPath)
	if !strings.HasPrefix(absPath, absBase+string(filepath.Separator)) {
		return "", "", "", errors.ErrForbidden
	}
	return physicalPath, entry.FileName, entry.MimeType, nil
}

func (s *Service) loadMessageAttachments(msg *model.Message) (*model.Message, error) {
	var attachments []model.MessageAttachment
	if err := s.db.Where("message_id = ?", msg.ID).Find(&attachments).Error; err != nil {
		// M2 fix: gracefully degrade when message_attachments table is missing
		// (e.g. due to migration gap). Return the message with empty attachments
		// instead of 500 — attachments are optional enrichment, not critical.
		msg.Attachments = nil
		return msg, nil
	}
	msg.Attachments = attachments
	return msg, nil
}

// CreateMessageText creates a text-only message (backward compatible helper).
func (s *Service) CreateMessageText(channelID, userID, username, role, content, clientMessageID string) (*model.Message, error) {
	return s.CreateMessage(channelID, userID, username, role, content, clientMessageID, "", "", nil, nil)
}

// GetMessage retrieves a message by ID
func (s *Service) GetMessage(messageID string) (*model.Message, error) {
	// C6: delegate to MessageRepository (cache-wrapped in production).
	msg, err := s.messageRepo.GetByID(context.Background(), messageID)
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errors.New(errors.MESSAGE_NOT_FOUND, "message not found")
		}
		return nil, errors.ErrInternal
	}
	return msg, nil
}

// AddReaction adds a reaction to a message
func (s *Service) AddReaction(messageID, userID, emoji string) (*model.Reaction, error) {
	// Check if message exists
	var msg model.Message
	if err := s.db.First(&msg, "id = ?", messageID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errors.New(errors.MESSAGE_NOT_FOUND, "message not found")
		}
		return nil, errors.ErrInternal
	}

	// Check if reaction already exists (idempotent)
	var existing model.Reaction
	if err := s.db.Where("message_id = ? AND user_id = ? AND emoji = ?", messageID, userID, emoji).First(&existing).Error; err == nil {
		return &existing, nil
	}

	reaction := &model.Reaction{
		ID:        idgen.GenerateID(idgen.PrefixMessage),
		MessageID: messageID,
		UserID:    userID,
		Emoji:     emoji,
	}

	if err := s.db.Create(reaction).Error; err != nil {
		return nil, errors.ErrInternal
	}
	return reaction, nil
}

// RemoveReaction removes a reaction from a message
func (s *Service) RemoveReaction(messageID, userID, emoji string) error {
	result := s.db.Where("message_id = ? AND user_id = ? AND emoji = ?", messageID, userID, emoji).Delete(&model.Reaction{})
	if result.Error != nil {
		return errors.ErrInternal
	}
	return nil
}

// GetReactions retrieves all reactions for a message
func (s *Service) GetReactions(messageID string) ([]model.Reaction, error) {
	var reactions []model.Reaction
	if err := s.db.Where("message_id = ?", messageID).Find(&reactions).Error; err != nil {
		return nil, errors.ErrInternal
	}
	return reactions, nil
}

// MarkChannelRead marks all messages in a channel as read for a user.
// A9 二期 R3：lastMessageID 若指向线程回复，水位回退到该回复之前最近的顶层消息；
// 找不到符合条件的顶层消息时水位不变（不写零值），避免把频道未读整体拉回 0。
func (s *Service) MarkChannelRead(userID, channelID, lastMessageID string) error {
	lastMessageID = strings.TrimSpace(lastMessageID)
	if lastMessageID != "" {
		var msg model.Message
		if err := s.db.Select("id, parent_id, created_at").First(&msg, "id = ?", lastMessageID).Error; err == nil {
			if msg.ParentID != nil && *msg.ParentID != "" {
				var fallback model.Message
				err := s.db.Select("id").
					Where("channel_id = ? AND parent_id IS NULL AND created_at <= ?", channelID, msg.CreatedAt).
					Order("created_at DESC").First(&fallback).Error
				if err != nil {
					if err == gorm.ErrRecordNotFound {
						return nil // 水位不变：线程回复之前没有顶层消息
					}
					return errors.ErrInternal
				}
				lastMessageID = fallback.ID
			}
		}
	}

	if err := s.db.Where("user_id = ? AND channel_id = ?", userID, channelID).Delete(&model.UserChannelRead{}).Error; err != nil {
		return err
	}
	read := &model.UserChannelRead{
		ID:                idgen.NextString(),
		UserID:            userID,
		ChannelID:         channelID,
		MessageID:         lastMessageID,
		LastReadMessageID: lastMessageID,
		UnreadCount:       0,
		MentionCount:      0,
		ReadAt:            time.Now().UTC(),
	}
	return s.db.Create(read).Error
}

// ThreadUnread 是线程维度的未读摘要（A9 二期，DES-20261029-01 §7.1）。
type ThreadUnread struct {
	ParentID string    `json:"parentId"`
	Count    int64     `json:"count"`
	LastAt   time.Time `json:"lastAt"`
}

// flexibleTime 兼容 SQLite 与 PostgreSQL 的时间聚合结果：
// PostgreSQL 的 MAX(timestamp) 返回 time.Time，而 SQLite（glebarez/sqlite）以
// TEXT 存储时间、MAX() 返回 string，直接 Scan 到 time.Time 会报
// "unsupported Scan, storing driver.Value type string into type *time.Time"。
type flexibleTime struct{ time.Time }

var flexibleTimeLayouts = []string{
	time.RFC3339Nano,
	"2006-01-02 15:04:05.999999999-07:00",
	"2006-01-02 15:04:05.999999999Z07:00",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02 15:04:05",
}

func (ft *flexibleTime) Scan(value interface{}) error {
	switch v := value.(type) {
	case nil:
		ft.Time = time.Time{}
		return nil
	case time.Time:
		ft.Time = v
		return nil
	case string:
		return ft.parse(v)
	case []byte:
		return ft.parse(string(v))
	default:
		return fmt.Errorf("flexibleTime: unsupported scan type %T", value)
	}
}

// Value 让 flexibleTime 同时满足 driver.Valuer，GORM 才会把它当成标量字段
// 而不是未知类型/关系字段（否则报 "invalid field found ... define a valid
// foreign key for relations or implement the Valuer/Scanner interface"）。
func (ft flexibleTime) Value() (driver.Value, error) {
	if ft.IsZero() {
		return nil, nil
	}
	return ft.Time, nil
}

func (ft *flexibleTime) parse(raw string) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		ft.Time = time.Time{}
		return nil
	}
	for _, layout := range flexibleTimeLayouts {
		if parsed, err := time.Parse(layout, raw); err == nil {
			ft.Time = parsed
			return nil
		}
	}
	return fmt.Errorf("flexibleTime: cannot parse %q", raw)
}

// UnreadInfo contains unread state for a user in a channel
type UnreadInfo struct {
	UnreadCount          int64          `json:"unreadCount"`
	MentionCount         int64          `json:"mentionCount"`
	FirstUnreadMessageID string         `json:"firstUnreadMessageId"`
	ThreadUnreads        []ThreadUnread `json:"threadUnreads"` // A9 二期：线程维度未读（旧客户端忽略未知字段）
}

// GetUnreadCount gets unread message info for a user in a channel.
// A9 二期语义（DES §6.1）：
//   - 频道未读 = 顶层消息（parent_id IS NULL）中 created_at > 频道水位（不排除本人，保持旧行为）；
//   - 线程未读 = 该 parent_id 下 created_at > COALESCE(线程读位, 频道水位) 且非本人所发。
func (s *Service) GetUnreadCount(userID, channelID string) (*UnreadInfo, error) {
	info := &UnreadInfo{ThreadUnreads: []ThreadUnread{}}

	// 频道水位：user_channel_reads 最后已读消息的 created_at；无记录或消息已删 = 零值时间。
	var watermark time.Time
	var lastRead model.UserChannelRead
	err := s.db.Where("user_id = ? AND channel_id = ?", userID, channelID).First(&lastRead).Error
	if err != nil {
		if err != gorm.ErrRecordNotFound {
			return nil, errors.ErrInternal
		}
	} else {
		info.MentionCount = int64(lastRead.MentionCount)
		var lastReadMsg model.Message
		if err := s.db.Select("created_at").First(&lastReadMsg, "id = ?", lastRead.LastReadMessageID).Error; err == nil {
			watermark = lastReadMsg.CreatedAt
		}
	}

	// 频道未读：只统计顶层消息（线程回复不计入频道未读）。
	channelUnreadQuery := func() *gorm.DB {
		q := s.db.Model(&model.Message{}).Where("channel_id = ? AND parent_id IS NULL", channelID)
		if !watermark.IsZero() {
			q = q.Where("created_at > ?", watermark)
		}
		return q
	}
	if err := channelUnreadQuery().Count(&info.UnreadCount).Error; err != nil {
		return nil, errors.ErrInternal
	}
	if info.UnreadCount > 0 {
		var firstUnread model.Message
		if err := channelUnreadQuery().Select("id").Order("created_at ASC").First(&firstUnread).Error; err == nil {
			info.FirstUnreadMessageID = firstUnread.ID
		}
	}

	// 线程未读聚合：单条 SQL（禁 N+1），按 parent_id 分组，lastAt 倒序，最多 20 条。
	type threadUnreadRow struct {
		ParentID string
		Cnt      int64
		LastAt   flexibleTime
	}
	var rows []threadUnreadRow
	if err := s.db.Table("messages AS m").
		Select("m.parent_id AS parent_id, COUNT(*) AS cnt, MAX(m.created_at) AS last_at").
		Joins("LEFT JOIN message_thread_reads AS tr ON tr.parent_id = m.parent_id AND tr.user_id = ?", userID).
		Where("m.channel_id = ? AND m.parent_id IS NOT NULL AND m.user_id != ? AND m.deleted_at IS NULL", channelID, userID).
		Where("m.created_at > COALESCE(tr.read_at, ?)", watermark).
		Group("m.parent_id").
		Order("last_at DESC").
		Limit(20).
		Scan(&rows).Error; err != nil {
		return nil, errors.ErrInternal
	}
	for _, r := range rows {
		info.ThreadUnreads = append(info.ThreadUnreads, ThreadUnread{ParentID: r.ParentID, Count: r.Cnt, LastAt: r.LastAt.Time})
	}

	return info, nil
}

// ThreadReadResult 线程已读写入结果（A9 二期 D-1 / S-01）。
// Advanced=false 表示本次上报没有推进水位（重复上报同一回复，或 lastMessageId
// 早于现有水位）；调用方此时不得广播清零事件，应返回 200 + "thread already read"。
type ThreadReadResult struct {
	LastReadMessageID string
	Advanced          bool
}

// MarkThreadRead 记录用户在某线程上的已读位（A9 二期，DES §7.2）。
// 校验失败统一返回 404 MESSAGE_NOT_FOUND（R2）：父消息不存在 / 不属于该频道 /
// 该线程没有回复。只 upsert message_thread_reads，绝不删除或修改 user_channel_reads。
//
// 水位语义（D-1/S-01）：read_at 取「目标回复的 created_at」，不再用 time.Now()。
// 客户端可能在 fetchThread 返回前就上报已读；若以上报时刻作水位，上报瞬间已存在
// 但客户端尚未渲染的回复会被算作已读，导致未读被静默清零。
// 单调性：read_at 只增不减（并发下由 upsert 的 CASE 保证原子性）；本次未推进水位时
// 返回 Advanced=false，由 handler 决定「不广播、返回 thread already read」。
func (s *Service) MarkThreadRead(userID, channelID, parentID, lastMessageID string) (*ThreadReadResult, error) {
	notFound := errors.New(errors.MESSAGE_NOT_FOUND, "message not found")
	var parent model.Message
	if err := s.db.Select("id, channel_id, parent_id").First(&parent, "id = ?", parentID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, notFound
		}
		return nil, errors.ErrInternal
	}
	if parent.ChannelID != channelID {
		return nil, notFound
	}
	var replies int64
	if err := s.db.Model(&model.Message{}).Where("parent_id = ?", parentID).Count(&replies).Error; err != nil {
		return nil, errors.ErrInternal
	}
	// 无可见回复（含回复已被全部删除/并发删除）同样按 R2 返回 404 MESSAGE_NOT_FOUND；
	// 客户端打开抽屉时若收到 404，应回退为「线程不存在/已清空」并刷新主列表。
	if replies == 0 {
		return nil, notFound
	}

	// 解析目标回复：一条查询同时完成「取 created_at」与「归属校验」。
	// 归属校验不可省（Low-1）：lastMessageId 若能指向同频道其它线程/顶层消息，
	// 就能把本线程水位抬到更晚时间，从而误清本线程未读。
	lastMessageID = strings.TrimSpace(lastMessageID)
	var target model.Message
	if lastMessageID != "" {
		if err := s.db.Select("id, created_at").Where("id = ? AND parent_id = ?", lastMessageID, parentID).First(&target).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return nil, notFound
			}
			return nil, errors.ErrInternal
		}
	} else {
		if err := s.db.Select("id, created_at").Where("parent_id = ?", parentID).Order("created_at DESC").First(&target).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return nil, notFound
			}
			return nil, errors.ErrInternal
		}
		lastMessageID = target.ID
	}
	watermarkAt := target.CreatedAt

	// 水位未推进（重复上报同一回复 / 更早的 lastMessageId）：直接返回，
	// 不写库也不广播，保证 read_at 与 last_read_message_id 均不回退。
	var existing model.MessageThreadRead
	switch err := s.db.Select("read_at", "last_read_message_id").
		Where("user_id = ? AND parent_id = ?", userID, parentID).First(&existing).Error; {
	case err == nil:
		if !watermarkAt.After(existing.ReadAt) {
			return &ThreadReadResult{LastReadMessageID: existing.LastReadMessageID, Advanced: false}, nil
		}
	case err == gorm.ErrRecordNotFound:
		// 首次上报该线程：继续写入。
	default:
		return nil, errors.ErrInternal
	}

	row := &model.MessageThreadRead{
		ID:                idgen.NextString(),
		UserID:            userID,
		ParentID:          parentID,
		ChannelID:         channelID,
		LastReadMessageID: lastMessageID,
		ReadAt:            watermarkAt,
	}
	// 用 CASE 而非 GREATEST：SQLite 无 GREATEST，CASE 在 SQLite/PG 通用。
	// 并发下两个上报可能都「未读到旧行」而通过上面的检查，CASE 保证最终落库值
	// 仍单调（只增不减），last_read_message_id 与 updated_at 随水位同向更新。
	err := s.db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "user_id"}, {Name: "parent_id"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"read_at":              gorm.Expr("CASE WHEN excluded.read_at > message_thread_reads.read_at THEN excluded.read_at ELSE message_thread_reads.read_at END"),
			"last_read_message_id": gorm.Expr("CASE WHEN excluded.read_at > message_thread_reads.read_at THEN excluded.last_read_message_id ELSE message_thread_reads.last_read_message_id END"),
			"updated_at":           gorm.Expr("CASE WHEN excluded.read_at > message_thread_reads.read_at THEN excluded.updated_at ELSE message_thread_reads.updated_at END"),
		}),
	}).Create(row).Error
	if err != nil {
		return nil, err
	}
	return &ThreadReadResult{LastReadMessageID: lastMessageID, Advanced: true}, nil
}

// ThreadReplyCounts 批量统计一组父消息的直接回复数（A9 二期 §7.7）。
// 一次 GROUP BY 查询，禁止按消息逐条计数（N+1）。
func (s *Service) ThreadReplyCounts(parentIDs []string) (map[string]int64, error) {
	out := make(map[string]int64, len(parentIDs))
	if len(parentIDs) == 0 {
		return out, nil
	}
	type countRow struct {
		ParentID string
		Cnt      int64
	}
	var rows []countRow
	if err := s.db.Table("messages AS m").
		Select("m.parent_id AS parent_id, COUNT(*) AS cnt").
		Where("m.parent_id IN ? AND m.deleted_at IS NULL", parentIDs).
		Group("m.parent_id").
		Scan(&rows).Error; err != nil {
		return nil, errors.ErrInternal
	}
	for _, r := range rows {
		out[r.ParentID] = r.Cnt
	}
	return out, nil
} // GetThread retrieves thread replies for a parent message
func (s *Service) GetThread(parentID string, limit int) ([]model.Message, bool, error) {
	if limit < 1 || limit > 100 {
		limit = 20
	}

	var messages []model.Message
	if err := s.db.Where("parent_id = ?", parentID).Order("created_at ASC").Limit(limit + 1).Find(&messages).Error; err != nil {
		return nil, false, errors.ErrInternal
	}

	hasMore := len(messages) > limit
	if hasMore {
		messages = messages[:limit]
	}

	// Batch query current user profiles
	if len(messages) > 0 {
		userIDs := make(map[string]struct{})
		for _, m := range messages {
			userIDs[m.UserID] = struct{}{}
		}
		uids := make([]string, 0, len(userIDs))
		for id := range userIDs {
			uids = append(uids, id)
		}
		var users []model.User
		if err := s.db.Select("id, username, display_name, avatar, role").Where("id IN ?", uids).Find(&users).Error; err == nil {
			userMap := make(map[string]*model.User)
			for i := range users {
				userMap[users[i].ID] = &users[i]
			}
			for i := range messages {
				if u, ok := userMap[messages[i].UserID]; ok {
					// Always overwrite AuthorDisplayName with the current display name
					// (renamed users update their historical messages).
					if u.DisplayName != "" {
						messages[i].AuthorDisplayName = u.DisplayName
					} else {
						messages[i].AuthorDisplayName = u.Username
					}
					messages[i].AuthorUsername = u.Username
					if messages[i].AuthorAvatarURL == "" {
						messages[i].AuthorAvatarURL = u.Avatar
					}
					if messages[i].AuthorRole == "" {
						messages[i].AuthorRole = u.Role
					}
				}
			}
		}
	}

	return messages, hasMore, nil
}

// RecordAck records a message acknowledgment
func (s *Service) RecordAck(messageID, userID, channelID string) error {
	ack := &model.MessageAck{
		ID:        idgen.GenerateID(idgen.PrefixMessage),
		MessageID: messageID,
		UserID:    userID,
		ChannelID: channelID,
	}
	// 使用 OnConflict DoNothing 处理重复 ACK，确保幂等
	// 唯一索引 idx_msg_ack_user_msg (message_id, user_id)
	return s.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "message_id"}, {Name: "user_id"}},
		DoNothing: true,
	}).Create(ack).Error
}

// PinMessage pins or unpins a message in a channel.
// 复用现有频道级置顶设计（channel.pinned_message_id，单条置顶）：
// pinned=true 时将频道置顶消息设为 messageID，pinned=false 时清空。
func (s *Service) PinMessage(channelID, messageID, userID string, pinned bool) error {
	// 校验消息存在且属于该频道
	var msg model.Message
	if err := s.db.First(&msg, "id = ? AND channel_id = ?", messageID, channelID).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return errors.New(errors.MESSAGE_NOT_FOUND, "message not found")
		}
		return errors.ErrInternal
	}

	// 更新频道置顶消息 ID
	var updateVal interface{}
	if pinned {
		updateVal = messageID
	} else {
		updateVal = nil
	}
	if err := s.db.Model(&model.Channel{}).Where("id = ?", channelID).Update("pinned_message_id", updateVal).Error; err != nil {
		return errors.ErrInternal
	}
	return nil
}
