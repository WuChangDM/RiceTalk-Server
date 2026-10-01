package cloudfs

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"ridgericetalk/core/crypto"
	"ridgericetalk/core/errors"
	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/database"
	"ridgericetalk/internal/logger"
	"ridgericetalk/internal/model"
)

// DES-2026-0912-05 云文件「分享链接」服务层。
//
// 这是全产品唯一一条能绕开 AuthRequired 取到文件内容的路径，因此本文件里的
// 每一条判定都对应设计文档的一个安全条款（下方注释标注了条款号）。改动前
// 请先读 文档/设计/DES-2026-0912-05-云文件分享链接安全设计.md。
const (
	// ShareDefaultTTLDays / ShareMaxTTLDays：过期时间必填，默认 7 天、上限 30 天
	// （设计 §6 与 §11 待确认项 1，按「上限 30 天」实施）。
	ShareDefaultTTLDays = 7
	ShareMaxTTLDays     = 30

	// ShareMaxPasswordLen 限制创建分享时接受的密码长度（bcrypt 只取前 72 字节，
	// 过长输入没有额外安全收益，只会放大 CPU 开销）。
	ShareMaxPasswordLen = 128

	// ShareTicketTTL 是「密码校验通过」后签发的一次性下载凭据的有效期（设计 §5）。
	ShareTicketTTL = 5 * time.Minute

	// shareTokenBytes 是分享令牌的熵：32 字节 = 256 bit（设计 §4）。
	shareTokenBytes = 32

	// shareTokenPrefixLen 是列表展示用的 token 前缀长度；**不参与任何校验**（设计 §2）。
	shareTokenPrefixLen = 8

	// ShareAudit* 是写入 SecurityAuditLog 的动作名（设计 §8）。
	ShareAuditCreated        = "cloudfs.share.created"
	ShareAuditRevoked        = "cloudfs.share.revoked"
	ShareAuditPasswordFailed = "cloudfs.share.password_failed"
	ShareAuditDownloadDenied = "cloudfs.share.download_denied"
	ShareAuditDownloaded     = "cloudfs.share.downloaded"

	// shareAuditDownloadSampleEvery 控制「成功下载」审计的采样率：第 1 次与每
	// 第 N 次记录，避免高频分享把审计表写爆（设计 §8「可采样」）。
	shareAuditDownloadSampleEvery = 10
)

// ShareOptions 是创建分享链接的入参。
type ShareOptions struct {
	// FileID 指向 cloudfs 文件（SharedFileEntry.ID）。接口不接受任何路径/文件名
	// 参数，只按该 ID 查库（设计 §3、§10「路径穿越」）。
	FileID string
	// ExpiresInDays 必填逻辑：0 表示使用默认 7 天；允许区间 [1, ShareMaxTTLDays]。
	ExpiresInDays int
	// Password 可选；空串表示无密码。
	Password string
	// MaxDownloads 0 = 不限次数；正数为上限（设计 §6）。
	MaxDownloads int
}

// ShareState 描述一个分享当前是否可用，以及不可用的原因。
type ShareState struct {
	Available bool
	Expired   bool
	Revoked   bool
	Exhausted bool
}

// Reason 返回不可用的机器可读原因（用于审计详情，不对外暴露）。
func (s ShareState) Reason() string {
	switch {
	case s.Revoked:
		return "revoked"
	case s.Expired:
		return "expired"
	case s.Exhausted:
		return "download_limit_reached"
	default:
		return "available"
	}
}

// GenerateShareToken 生成一个新的分享令牌，返回明文令牌、其 SHA-256 十六进制
// 摘要与展示用前缀。明文只在此刻存在，之后不再可获取（设计 §2/§4）。
func GenerateShareToken() (token, tokenHash, prefix string, err error) {
	buf := make([]byte, shareTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		// crypto/rand 失败意味着进程环境没有可用熵源，不能降级成弱随机。
		return "", "", "", errors.ErrInternal
	}
	token = base64.RawURLEncoding.EncodeToString(buf)
	return token, HashShareToken(token), shareTokenPrefix(token), nil
}

// HashShareToken 返回令牌的 SHA-256 十六进制摘要。数据库里只存这个值（设计 §2/§4）。
func HashShareToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func shareTokenPrefix(token string) string {
	if len(token) <= shareTokenPrefixLen {
		return token
	}
	return token[:shareTokenPrefixLen]
}

// CreateShare 为空间内的单个文件创建分享链接，返回分享记录与**仅此一次**的明文令牌。
//
// 权限（设计 §5/§11-3）：复用 cloudfs 既有的成员可见性判定（requireMembership），
// 即「文件可见范围内的成员均可创建」，不另写一套规则。
func (s *Service) CreateShare(spaceID, userID string, opts ShareOptions) (*model.CloudFileShare, string, error) {
	if err := s.requireMembership(spaceID, userID); err != nil {
		return nil, "", err
	}

	opts.FileID = strings.TrimSpace(opts.FileID)
	if opts.FileID == "" {
		return nil, "", errors.ErrBadRequest
	}

	// 只按 FileID + spaceID 查库，绝不接受调用方给出的路径；
	// GORM 的软删除作用域同时保证已删除的文件无法被分享。
	var entry model.SharedFileEntry
	if err := s.db.Where("id = ? AND space_id = ?", opts.FileID, spaceID).First(&entry).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, "", errors.New(errors.CLOUDFS_FILE_NOT_FOUND, "file not found")
		}
		return nil, "", errors.ErrInternal
	}

	days := opts.ExpiresInDays
	if days == 0 {
		days = ShareDefaultTTLDays
	}
	if days < 1 || days > ShareMaxTTLDays {
		return nil, "", errors.New(errors.SHARE_TTL_INVALID,
			fmt.Sprintf("expiresInDays must be between 1 and %d", ShareMaxTTLDays))
	}
	if opts.MaxDownloads < 0 {
		return nil, "", errors.New(errors.SHARE_MAX_DOWNLOADS_INVALID, "maxDownloads must not be negative")
	}
	if len(opts.Password) > ShareMaxPasswordLen {
		return nil, "", errors.New(errors.SHARE_PASSWORD_INVALID, "password too long")
	}

	var passwordHash string
	if opts.Password != "" {
		// 复用项目统一的口令存储（bcrypt，cost 由配置驱动），不引入第二种哈希。
		hash, err := crypto.HashPassword(opts.Password)
		if err != nil {
			return nil, "", errors.ErrInternal
		}
		passwordHash = hash
	}

	token, tokenHash, prefix, err := GenerateShareToken()
	if err != nil {
		return nil, "", err
	}

	now := time.Now().UTC()
	share := &model.CloudFileShare{
		ID:           idgen.GenerateID(idgen.PrefixShare),
		FileID:       entry.ID,
		SpaceID:      spaceID,
		OwnerID:      userID,
		TokenHash:    tokenHash,
		TokenPrefix:  prefix,
		PasswordHash: passwordHash,
		ExpiresAt:    now.AddDate(0, 0, days),
		MaxDownloads: opts.MaxDownloads,
		CreatedAt:    now,
	}
	if err := s.db.Create(share).Error; err != nil {
		return nil, "", database.ClassifyError(err)
	}
	return share, token, nil
}

// ListShares 列出空间内的分享（可按 fileID 过滤）。
//
// 空间成员的可见性由 requireMembership 统一保证；TokenHash/PasswordHash 带
// `json:"-"`，绝不会出现在响应里。
func (s *Service) ListShares(spaceID, userID, fileID string) ([]model.CloudFileShare, error) {
	if err := s.requireMembership(spaceID, userID); err != nil {
		return nil, err
	}
	q := s.db.Where("space_id = ?", spaceID)
	if fileID != "" {
		q = q.Where("file_id = ?", fileID)
	}
	var shares []model.CloudFileShare
	if err := q.Order("created_at DESC").Limit(200).Find(&shares).Error; err != nil {
		return nil, errors.ErrInternal
	}
	return shares, nil
}

// ShareFileNames 批量解析分享指向的文件名，供列表展示。找不到（文件已删除）
// 的 ID 不会出现在结果里。
func (s *Service) ShareFileNames(shares []model.CloudFileShare) map[string]string {
	ids := make([]string, 0, len(shares))
	seen := make(map[string]bool, len(shares))
	for _, sh := range shares {
		if !seen[sh.FileID] {
			seen[sh.FileID] = true
			ids = append(ids, sh.FileID)
		}
	}
	names := make(map[string]string, len(ids))
	if len(ids) == 0 {
		return names
	}
	var entries []model.SharedFileEntry
	if err := s.db.Select("id, file_name").Where("id IN ?", ids).Find(&entries).Error; err != nil {
		return names
	}
	for _, e := range entries {
		names[e.ID] = e.FileName
	}
	return names
}

// RevokeShare 撤销分享：只有创建者本人或空间管理员可以操作（设计 §1「分享者可
// 随时撤销」+ 越权防护）。
//
// 撤销只写 revoked_at，且不提供任何清除路径 ——「已撤销的 token 永不复活」（设计 §6）。
func (s *Service) RevokeShare(spaceID, userID, shareID string) (*model.CloudFileShare, error) {
	if err := s.requireMembership(spaceID, userID); err != nil {
		return nil, err
	}
	if strings.TrimSpace(shareID) == "" {
		return nil, errors.ErrBadRequest
	}

	var share model.CloudFileShare
	if err := s.db.Where("id = ? AND space_id = ?", shareID, spaceID).First(&share).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errors.New(errors.SHARE_NOT_FOUND, "share not found")
		}
		return nil, errors.ErrInternal
	}
	if share.OwnerID != userID && !s.isSpaceAdmin(spaceID, userID) {
		return nil, errors.New(errors.CLOUDFS_PERMISSION_DENIED, "no permission to revoke this share")
	}

	if share.RevokedAt == nil {
		now := time.Now().UTC()
		// 条件更新：并发重复撤销只会成功一次，且永远不会把 revoked_at 清空。
		if err := s.db.Model(&model.CloudFileShare{}).
			Where("id = ? AND revoked_at IS NULL", share.ID).
			Update("revoked_at", now).Error; err != nil {
			return nil, errors.ErrInternal
		}
		share.RevokedAt = &now
	}
	return &share, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// E4：admin 全局分享管理（DES-20261001-01 §11.2）
// ─────────────────────────────────────────────────────────────────────────────

// AdminShareItem 是 admin 全局分享列表的单行视图（联出文件名与空间名）。
type AdminShareItem struct {
	ID               string     `json:"id"`
	FileID           string     `json:"fileId"`
	FileName         string     `json:"fileName"` // 文件软删/缺失时为空串
	UserID           string     `json:"userId"`   // 分享属主（OwnerID）
	SpaceID          string     `json:"spaceId"`
	SpaceName        string     `json:"spaceName"` // 空间软删/缺失时为空串
	DownloadCount    int        `json:"downloadCount"`
	LastAccessAt     *time.Time `json:"lastAccessAt,omitempty"`
	Revoked          bool       `json:"revoked"`
	Expired          bool       `json:"expired"`
	RequiresPassword bool       `json:"requiresPassword"`
	ExpiresAt        time.Time  `json:"expiresAt"`
	CreatedAt        time.Time  `json:"createdAt"`
}

// AdminListShares 跨**全部空间**列出分享（admin 专用，不走 requireMembership）。
//
// 口径与既有 ListShares 一致：不过滤 revoked/expired（由响应字段呈现状态），
// 按 created_at DESC 排序；文件名/空间名走 GORM 默认软删作用域的批量查询
// （与 ShareFileNames 相同模式），软删/缺失显示空串，绝不使用原生 JOIN。
func (s *Service) AdminListShares(limit, offset int) ([]AdminShareItem, error) {
	var shares []model.CloudFileShare
	if err := s.db.Order("created_at DESC").Limit(limit).Offset(offset).Find(&shares).Error; err != nil {
		return nil, errors.ErrInternal
	}

	names := s.ShareFileNames(shares)

	// 批量解析空间名（Space 软删行被 GORM 默认作用域过滤，对应 spaceName 留空）。
	spaceIDs := make([]string, 0, len(shares))
	seenSpace := make(map[string]bool, len(shares))
	for _, sh := range shares {
		if !seenSpace[sh.SpaceID] {
			seenSpace[sh.SpaceID] = true
			spaceIDs = append(spaceIDs, sh.SpaceID)
		}
	}
	spaceNames := make(map[string]string, len(spaceIDs))
	if len(spaceIDs) > 0 {
		var spaces []model.Space
		if err := s.db.Select("id, name").Where("id IN ?", spaceIDs).Find(&spaces).Error; err == nil {
			for _, sp := range spaces {
				spaceNames[sp.ID] = sp.Name
			}
		}
	}

	now := time.Now().UTC()
	items := make([]AdminShareItem, 0, len(shares))
	for i := range shares {
		sh := shares[i]
		items = append(items, AdminShareItem{
			ID:               sh.ID,
			FileID:           sh.FileID,
			FileName:         names[sh.FileID],
			UserID:           sh.OwnerID,
			SpaceID:          sh.SpaceID,
			SpaceName:        spaceNames[sh.SpaceID],
			DownloadCount:    sh.DownloadCount,
			LastAccessAt:     sh.LastAccessAt,
			Revoked:          sh.RevokedAt != nil,
			Expired:          !sh.ExpiresAt.After(now),
			RequiresPassword: sh.PasswordHash != "",
			ExpiresAt:        sh.ExpiresAt,
			CreatedAt:        sh.CreatedAt,
		})
	}
	return items, nil
}

// RevokeShareAsAdmin 以全局管理员身份撤销分享（E4）。与空间内 RevokeShare 相同的
// 核心语义：
//   - 分享不存在返回 SHARE_NOT_FOUND；
//   - 条件更新 `revoked_at IS NULL` 才写入，并发重复撤销只成功一次，永不复活；
//   - 幂等：已撤销的分享再次撤销直接返回既有记录，revoked=false 表示本次无新写入。
//
// 返回值 revoked 表示**本次调用**是否实际执行了撤销（用于审计只在首次撤销时落一条）。
func (s *Service) RevokeShareAsAdmin(shareID string) (*model.CloudFileShare, bool, error) {
	if strings.TrimSpace(shareID) == "" {
		return nil, false, errors.ErrBadRequest
	}

	var share model.CloudFileShare
	if err := s.db.Where("id = ?", shareID).First(&share).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, false, errors.New(errors.SHARE_NOT_FOUND, "share not found")
		}
		return nil, false, errors.ErrInternal
	}

	revoked := false
	if share.RevokedAt == nil {
		now := time.Now().UTC()
		// 条件更新：与 RevokeShare 完全一致，并发重复撤销只会成功一次。
		if err := s.db.Model(&model.CloudFileShare{}).
			Where("id = ? AND revoked_at IS NULL", share.ID).
			Update("revoked_at", now).Error; err != nil {
			return nil, false, errors.ErrInternal
		}
		share.RevokedAt = &now
		revoked = true
	}
	return &share, revoked, nil
}

// GetShareByToken 按明文令牌解析分享记录。令牌不存在时返回 SHARE_NOT_FOUND。
//
// 查库走 token_hash 唯一索引（明文永不落库），命中后再做一次常量时间比对作为
// 纵深防御：即使未来索引/排序规则变成大小写不敏感，也不会出现「非精确匹配即通过」。
func (s *Service) GetShareByToken(token string) (*model.CloudFileShare, error) {
	if token == "" {
		return nil, errors.New(errors.SHARE_NOT_FOUND, "share not found")
	}
	hash := HashShareToken(token)
	var share model.CloudFileShare
	if err := s.db.Where("token_hash = ?", hash).First(&share).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errors.New(errors.SHARE_NOT_FOUND, "share not found")
		}
		return nil, errors.ErrInternal
	}
	if subtle.ConstantTimeCompare([]byte(share.TokenHash), []byte(hash)) != 1 {
		return nil, errors.New(errors.SHARE_NOT_FOUND, "share not found")
	}
	return &share, nil
}

// EvaluateShare 判断分享当前是否可用（过期 / 撤销 / 次数用尽）。
//
// 注意：这只是**展示与非关键判定**用的读时判断。真正决定「这一次下载能否发生」
// 的必须是 ClaimShareDownload 的原子 UPDATE（设计 §6）。
func (s *Service) EvaluateShare(share *model.CloudFileShare) ShareState {
	now := time.Now().UTC()
	state := ShareState{}
	if share.RevokedAt != nil {
		state.Revoked = true
	}
	if !share.ExpiresAt.After(now) {
		state.Expired = true
	}
	if share.MaxDownloads > 0 && share.DownloadCount >= share.MaxDownloads {
		state.Exhausted = true
	}
	state.Available = !state.Revoked && !state.Expired && !state.Exhausted
	return state
}

// ClaimShareDownload 原子地占用一次下载配额，返回占用后的下载计数。
//
// 设计 §6 的核心：**先原子占用、再返回文件**。判定条件全部写在 WHERE 里，因此
// 并发调用不可能超卖 —— 受影响行数为 0 即代表「已过期 / 已撤销 / 已达上限」。
// 若写成「先 SELECT 检查再 UPDATE 递增」，并发下两个请求可以同时读到
// download_count < max 而各自递增，从而突破上限。
//
// 时间统一用 UTC 传入，使 SQLite（TEXT 字典序）与 PostgreSQL（timestamptz）
// 上的 `expires_at > ?` 比较语义一致。
func (s *Service) ClaimShareDownload(shareID string) (int, error) {
	now := time.Now().UTC()
	res := s.db.Exec(`UPDATE cloud_file_shares
		SET download_count = download_count + 1, last_access_at = ?
		WHERE id = ? AND revoked_at IS NULL AND expires_at > ?
		  AND (max_downloads = 0 OR download_count < max_downloads)`,
		now, shareID, now)
	if res.Error != nil {
		return 0, errors.ErrInternal
	}
	if res.RowsAffected == 0 {
		// 占位失败：读一次当前行只是为了给出准确的拒绝原因（审计与错误码），
		// 这个读不参与判定，因此不会重新引入「先检查再递增」的竞态。
		var share model.CloudFileShare
		if err := s.db.Where("id = ?", shareID).First(&share).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				return 0, errors.New(errors.SHARE_NOT_FOUND, "share not found")
			}
			return 0, errors.ErrInternal
		}
		return 0, shareRejectionError(&share, now)
	}

	var share model.CloudFileShare
	if err := s.db.Select("download_count").Where("id = ?", shareID).First(&share).Error; err != nil {
		return 0, errors.ErrInternal
	}
	return share.DownloadCount, nil
}

// shareRejectionError 把「占位失败」翻译成具体的拒绝错误码。竞态落败（上面三个
// 条件在读的时候都已不成立）时返回兜底的 SHARE_UNAVAILABLE，不谎报原因。
func shareRejectionError(share *model.CloudFileShare, now time.Time) error {
	switch {
	case share.RevokedAt != nil:
		return errors.New(errors.SHARE_REVOKED, "share has been revoked")
	case !share.ExpiresAt.After(now):
		return errors.New(errors.SHARE_EXPIRED, "share has expired")
	case share.MaxDownloads > 0 && share.DownloadCount >= share.MaxDownloads:
		return errors.New(errors.SHARE_DOWNLOAD_LIMITED, "download limit reached")
	default:
		return errors.New(errors.SHARE_UNAVAILABLE, "share is not available")
	}
}

// shareFileEntry 取分享指向的文件记录。
//
// 只按 (id, space_id) 查库、不接受任何调用方路径；GORM 软删除作用域使「已删除 /
// 已进回收站」的文件在此处直接变成 not found（设计 §3、§10）。
func (s *Service) shareFileEntry(share *model.CloudFileShare) (*model.SharedFileEntry, error) {
	var entry model.SharedFileEntry
	if err := s.db.Where("id = ? AND space_id = ?", share.FileID, share.SpaceID).First(&entry).Error; err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, errors.New(errors.CLOUDFS_FILE_NOT_FOUND, "file not found")
		}
		return nil, errors.ErrInternal
	}
	return &entry, nil
}

// shareDownloadInfo 解析分享文件在磁盘上的位置。
//
// 与 Service.DownloadInfo 的差别只有两点：不再要求空间成员身份（调用方已持令牌），
// 以及**不做**「按 ID 前缀扫描目录」的历史兼容回退 —— 公开接口上的路径解析越少越好，
// 老记录（PhysicalName 为空）一律按文件不存在处理。
func (s *Service) shareDownloadInfo(entry *model.SharedFileEntry) (*DownloadInfo, error) {
	if entry.PhysicalName == "" {
		return nil, errors.New(errors.CLOUDFS_FILE_NOT_FOUND, "file not found")
	}
	baseDir := s.baseDir()
	physicalPath := filepath.Join(baseDir, entry.PhysicalName)
	// 与既有下载路径同一口径：解析后的路径必须仍在 cloudfs 根目录内。
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

// LogShareSecurityEvent 把分享相关动作写入防篡改的 SecurityAuditLog（设计 §8）。
//
// 审计失败绝不能影响请求本身，因此返回值被有意丢弃；IP 在写库前做掩码处理，
// 与 auth / admin 模块的既有做法一致。明文 token 与密码永不写入审计。
func (s *Service) LogShareSecurityEvent(userID, action, shareID, ip, userAgent string, details map[string]interface{}) {
	log := &model.SecurityAuditLog{
		ID:           idgen.GenerateID(idgen.PrefixUser),
		UserID:       userID,
		Action:       action,
		ResourceType: "cloud_file_share",
		ResourceID:   shareID,
		IPMasked:     logger.MaskIPForLog(ip),
		CreatedAt:    time.Now().UTC(),
	}
	if userAgent != "" {
		if details == nil {
			details = make(map[string]interface{})
		}
		details["userAgent"] = userAgent
	}
	if len(details) > 0 {
		if b, err := json.Marshal(details); err == nil {
			log.DetailsJSON = string(b)
		}
	}
	_ = s.db.Create(log).Error
}

// ShouldAuditDownload 决定第 count 次成功下载是否需要写审计（采样，设计 §8）。
func ShouldAuditDownload(count int) bool {
	return count <= 1 || count%shareAuditDownloadSampleEvery == 0
}

// ─────────────────────────────────────────────────────────────────────────────
// 一次性下载凭据（密码校验通过后签发，设计 §5）
// ─────────────────────────────────────────────────────────────────────────────

// shareTicket 是一次性下载凭据。只存在于内存：进程重启后失效只是让用户重新
// 输一次密码；同时它也绝不会落库或写进审计日志。
type shareTicket struct {
	shareID   string
	expiresAt time.Time
}

// shareTicketStore 保存凭据的哈希 -> 凭据。与限流器、爆破防护一样是进程内状态
// （本产品按单实例自托管设计）。
type shareTicketStore struct {
	mu        sync.Mutex
	tickets   map[string]shareTicket
	lastSweep time.Time
}

func newShareTicketStore() *shareTicketStore {
	return &shareTicketStore{tickets: make(map[string]shareTicket)}
}

// Issue 为 shareID 签发一张一次性凭据，返回明文凭据（只在本次响应里出现）。
func (st *shareTicketStore) Issue(shareID string) (string, error) {
	buf := make([]byte, shareTokenBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", errors.ErrInternal
	}
	ticket := base64.RawURLEncoding.EncodeToString(buf)
	now := time.Now()
	st.mu.Lock()
	defer st.mu.Unlock()
	st.sweepLocked(now)
	st.tickets[HashShareToken(ticket)] = shareTicket{shareID: shareID, expiresAt: now.Add(ShareTicketTTL)}
	return ticket, nil
}

// Consume 校验并作废一张凭据。凭据绑定单个分享且只能用一次：无论后续校验是否
// 通过都先删除，避免「猜中即复用」。
func (st *shareTicketStore) Consume(shareID, ticket string) bool {
	if ticket == "" {
		return false
	}
	key := HashShareToken(ticket)
	st.mu.Lock()
	defer st.mu.Unlock()
	entry, ok := st.tickets[key]
	if !ok {
		return false
	}
	delete(st.tickets, key)
	if entry.shareID != shareID {
		return false
	}
	return time.Now().Before(entry.expiresAt)
}

// sweepLocked 周期性清理过期凭据，避免无界增长。最多每分钟扫一次，保证
// Issue 的均摊开销与凭据数量无关。
func (st *shareTicketStore) sweepLocked(now time.Time) {
	if now.Sub(st.lastSweep) < time.Minute {
		return
	}
	st.lastSweep = now
	for key, entry := range st.tickets {
		if now.After(entry.expiresAt) {
			delete(st.tickets, key)
		}
	}
}
