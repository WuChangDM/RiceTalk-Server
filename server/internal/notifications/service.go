// Package notifications 实现通知中心的服务端半边（1.9.4 task-118）：
// 通知落库（@提及 / 私信）、列表拉取（游标分页 + 未读过滤）、单条 / 全部已读。
//
// 设计要点：
//   - 通知是**持久状态**（表 notifications，迁移 000047）：服务端重启后仍能拉到，
//     不依赖 WebSocket 推送（推送只是即时提示，通知本身落库）；
//   - 写入是**副作用**：任何写通知失败都不影响触发它的业务动作（发消息）；
//   - 读取一律按 user_id 过滤，绝不返回他人通知；跨用户标记已读返回 403。
package notifications

import (
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"

	"ridgericetalk/core/errors"
	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/model"
)

// 通知类型（model.Notification.Type）。
const (
	TypeMention = "mention" // 频道消息里 @了自己
	TypeDM      = "dm"      // 收到私信
	TypeInvite  = "invite"  // 邀请（预留）
	TypeSystem  = "system"  // 系统公告（预留）
)

// 通知来源（model.Notification.RefType）。
const (
	RefTypeMessage   = "message"    // RefID = 消息 ID（mention）
	RefTypeDMChannel = "dm_channel" // RefID = 私信频道 ID（dm）
)

const (
	// DefaultListLimit 是 GET /notifications 未传 limit 时的默认条数。
	DefaultListLimit = 20
	// MaxListLimit 是单次拉取上限，防止客户端一次拉全量。
	MaxListLimit = 100
	// previewMaxRunes 是通知正文（消息摘要）最大字符数，超出截断加省略号。
	previewMaxRunes = 80
)

// Service 负责通知的落库与查询。
type Service struct {
	db *gorm.DB
}

// NewService 构造通知服务。
func NewService(db *gorm.DB) *Service {
	return &Service{db: db}
}

// Item 是 GET /notifications 返回的单条通知（字段只增不减：客户端已按此渲染）。
type Item struct {
	ID        string     `json:"id"`
	Type      string     `json:"type"`
	Title     string     `json:"title"`
	Body      string     `json:"body"`
	RefType   string     `json:"refType"`
	RefID     string     `json:"refId"`
	SpaceID   string     `json:"spaceId,omitempty"`
	ReadAt    *time.Time `json:"readAt"`
	CreatedAt time.Time  `json:"createdAt"`
}

// ListOptions 是列表查询参数。
type ListOptions struct {
	UnreadOnly bool
	Limit      int
	Cursor     string
}

// ListResult 是 GET /notifications 的响应体。
type ListResult struct {
	Items       []Item `json:"notifications"`
	UnreadCount int64  `json:"unreadCount"`
	NextCursor  string `json:"nextCursor"`
}

// Create 落库一条通知；未给 ID 时自动生成带前缀 ID。
func (s *Service) Create(n *model.Notification) error {
	if s == nil || s.db == nil || n == nil || n.UserID == "" {
		return nil
	}
	if n.ID == "" {
		n.ID = idgen.GenerateID(idgen.PrefixNotification)
	}
	return s.db.Create(n).Error
}

// NotifyMentionForMessage 为一条频道消息的单个被提及者落库 mention 通知。
// 调用方负责跳过作者本人；spaceID 由频道反查（失败留空，不影响通知本身）。
func (s *Service) NotifyMentionForMessage(msg *model.Message, recipientID, fromDisplay string) error {
	if s == nil || s.db == nil || msg == nil || recipientID == "" {
		return nil
	}
	title := "有人提到了你"
	if fromDisplay != "" {
		title = fromDisplay + " 提到了你"
	}
	return s.Create(&model.Notification{
		UserID:  recipientID,
		Type:    TypeMention,
		Title:   title,
		Body:    previewText(msg.Content),
		RefType: RefTypeMessage,
		RefID:   msg.ID,
		SpaceID: s.channelSpaceID(msg.ChannelID),
	})
}

// NotifyDM 为一条私信消息落库 dm 通知（收件人为会话另一方）。
func (s *Service) NotifyDM(msg *model.Message, recipientID, fromDisplay string) error {
	if s == nil || s.db == nil || msg == nil || recipientID == "" {
		return nil
	}
	title := "你收到一条私信"
	if fromDisplay != "" {
		title = fromDisplay + " 给你发了一条私信"
	}
	return s.Create(&model.Notification{
		UserID:  recipientID,
		Type:    TypeDM,
		Title:   title,
		Body:    previewText(msg.Content),
		RefType: RefTypeDMChannel,
		RefID:   msg.ChannelID,
	})
}

// List 返回 userID 的通知列表（created_at DESC, id DESC），并附带未读数与该页游标。
//
// 游标语义：cursor = 上一页最后一条通知的 ID；下一页从该条之后（更旧）继续。
// 未读数不受 UnreadOnly / 分页影响，始终是该用户当前全部未读数。
func (s *Service) List(userID string, opts ListOptions) (*ListResult, error) {
	limit := opts.Limit
	if limit <= 0 {
		limit = DefaultListLimit
	}
	if limit > MaxListLimit {
		limit = MaxListLimit
	}

	q := s.db.Model(&model.Notification{}).Where("user_id = ?", userID)
	if opts.UnreadOnly {
		q = q.Where("read_at IS NULL")
	}
	if opts.Cursor != "" {
		var cur model.Notification
		if err := s.db.Select("id", "created_at").
			Where("id = ? AND user_id = ?", opts.Cursor, userID).
			First(&cur).Error; err != nil {
			return nil, errors.New(errors.SYSTEM_BAD_REQUEST, "invalid cursor")
		}
		q = q.Where("created_at < ? OR (created_at = ? AND id < ?)", cur.CreatedAt, cur.CreatedAt, cur.ID)
	}

	rows := make([]model.Notification, 0, limit+1)
	if err := q.Order("created_at DESC, id DESC").Limit(limit + 1).Find(&rows).Error; err != nil {
		return nil, err
	}

	nextCursor := ""
	if len(rows) > limit {
		rows = rows[:limit]
		nextCursor = rows[len(rows)-1].ID
	}

	items := make([]Item, 0, len(rows))
	for i := range rows {
		items = append(items, toItem(&rows[i]))
	}

	var unread int64
	if err := s.db.Model(&model.Notification{}).
		Where("user_id = ? AND read_at IS NULL", userID).
		Count(&unread).Error; err != nil {
		return nil, err
	}

	return &ListResult{Items: items, UnreadCount: unread, NextCursor: nextCursor}, nil
}

// MarkRead 把一条通知标为已读（幂等：已读后再调用不改首次已读时间）。
// 通知不存在 → NOTIFICATION_NOT_FOUND(404)；通知属于他人 → 403。
func (s *Service) MarkRead(userID, notificationID string) (*model.Notification, error) {
	var n model.Notification
	if err := s.db.Where("id = ?", notificationID).First(&n).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errors.New(errors.NOTIFICATION_NOT_FOUND, "notification not found")
		}
		return nil, err
	}
	if n.UserID != userID {
		// 不泄露「该 ID 存在」：跨用户一律 403（与 task-116 附件/消息鉴权口径一致）。
		return nil, errors.ErrForbidden
	}
	if n.ReadAt == nil {
		now := time.Now()
		if err := s.db.Model(&model.Notification{}).
			Where("id = ? AND user_id = ? AND read_at IS NULL", n.ID, userID).
			Update("read_at", now).Error; err != nil {
			return nil, err
		}
		n.ReadAt = &now
	}
	return &n, nil
}

// MarkAllRead 把 userID 的全部未读通知标为已读，返回受影响条数（幂等）。
func (s *Service) MarkAllRead(userID string) (int64, error) {
	res := s.db.Model(&model.Notification{}).
		Where("user_id = ? AND read_at IS NULL", userID).
		Update("read_at", time.Now())
	return res.RowsAffected, res.Error
}

// channelSpaceID 反查频道所属 Space；失败返回空串（通知照常落库）。
func (s *Service) channelSpaceID(channelID string) string {
	if s == nil || s.db == nil || channelID == "" {
		return ""
	}
	var spaceID string
	if err := s.db.Model(&model.Channel{}).
		Select("space_id").
		Where("id = ?", channelID).
		Limit(1).
		Scan(&spaceID).Error; err != nil {
		return ""
	}
	return spaceID
}

// previewText 生成通知正文摘要（按 rune 截断，避免切断多字节字符）。
func previewText(content string) string {
	content = strings.TrimSpace(content)
	runes := []rune(content)
	if len(runes) <= previewMaxRunes {
		return content
	}
	return string(runes[:previewMaxRunes]) + "…"
}

// toItem 把模型转换为响应结构。
func toItem(n *model.Notification) Item {
	return Item{
		ID:        n.ID,
		Type:      n.Type,
		Title:     n.Title,
		Body:      n.Body,
		RefType:   n.RefType,
		RefID:     n.RefID,
		SpaceID:   n.SpaceID,
		ReadAt:    n.ReadAt,
		CreatedAt: n.CreatedAt,
	}
}

// ParseLimit 解析 limit 查询参数：非法/缺失回落默认值，超上限截断。
func ParseLimit(raw string) int {
	if raw == "" {
		return DefaultListLimit
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return DefaultListLimit
	}
	if n > MaxListLimit {
		return MaxListLimit
	}
	return n
}

// ParseBoolQuery 解析 "1"/"true"/"yes" 等布尔查询参数。
func ParseBoolQuery(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}
