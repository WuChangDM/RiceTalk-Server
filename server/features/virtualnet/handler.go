package virtualnet

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"ridgericetalk/core/errors"
	"ridgericetalk/core/httpbind"
	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/config"
	"ridgericetalk/internal/model"
	"ridgericetalk/internal/realtime"
	"ridgericetalk/middleware"
)

// Handler handles virtual network HTTP requests
//
// DES-2026-0731-02: 改造为 EasyTier 控制平面。
// 服务端不再像 Headscale 那样管理 WireGuard 节点注册，而是：
//   1. 健康检查：通过 HTTP GET /api/v1/health 检查 EasyTier 服务可用性
//   2. 凭证下发：将 network_name + network_secret 下发给客户端
//   3. 元数据存储：在数据库记录会话与节点信息（仅用于状态展示）
//   4. 节点查询：可选通过 EasyTier Web API 拉取实时节点列表（10.126.126.0/24 网络内）
//
// 客户端收到凭证后，在本地启动 easytier-core 子进程加入网络，服务端不参与数据平面。
type Handler struct {
	db  *gorm.DB
	cfg *config.Config
	hub *realtime.Hub
}

// NewHandler creates a new virtualnet handler
func NewHandler(db *gorm.DB, cfg *config.Config, hub *realtime.Hub) *Handler {
	return &Handler{db: db, cfg: cfg, hub: hub}
}

func (h *Handler) requireSpaceMembership(c *gin.Context, spaceID string) error {
	userID := middleware.GetUserID(c)
	var count int64
	if err := h.db.Model(&model.Membership{}).Where("space_id = ? AND user_id = ?", spaceID, userID).Count(&count).Error; err != nil {
		return errors.ErrInternal
	}
	if count == 0 {
		return errors.ErrForbidden.WithDetails("no permission to access virtual network")
	}
	return nil
}

// RegisterRoutes registers virtualnet routes
func (h *Handler) RegisterRoutes(r *gin.RouterGroup) {
	vn := r.Group("/virtualnet")
	vn.Use(middleware.AuthRequired(h.cfg, h.db))
	{
		vn.GET("/status", h.GetStatus)
		vn.POST("/connect", h.Connect)
		vn.POST("/disconnect", h.Disconnect)
		vn.GET("/nodes", h.GetNodes)
		vn.POST("/report-ip", h.ReportIP) // 客户端上报虚拟 IP
		// Keep legacy routes for backward compatibility
		vn.POST("/join", h.Connect)
		vn.POST("/leave", h.Disconnect)
	}
}

// extractHost 从地址字符串中提取纯 host（IP 或域名），去除协议和端口。
// 例如："http://<REDACTED-PRODUCTION-IP>:5000" -> "<REDACTED-PRODUCTION-IP>"
//       "<REDACTED-PRODUCTION-IP>" -> "<REDACTED-PRODUCTION-IP>"
//       "https://example.com:8443" -> "example.com"
func extractHost(addr string) string {
	if addr == "" {
		return ""
	}
	// 如果包含协议前缀，用 url.Parse 解析
	if strings.Contains(addr, "://") {
		u, err := url.Parse(addr)
		if err == nil && u.Hostname() != "" {
			return u.Hostname()
		}
	}
	// 否则去除可能的端口部分
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return addr
}

// isEasytierAvailable checks if EasyTier service is configured and reachable.
// 服务端在启动时由 deploy-baremetal.sh vpn stage 部署 EasyTier systemd 服务，
// RPC Portal 默认监听 127.0.0.1:11210（gRPC 协议，不支持 HTTP/1.1 REST）。
// 此处通过 TCP 连接探测端口是否监听，不依赖 HTTP 协议。
func (h *Handler) isEasytierAvailable() bool {
	if h.cfg.EasytierURL == "" || h.cfg.EasytierSecret == "" {
		return false
	}
	// 从 EasytierURL（如 http://127.0.0.1:11210）中提取 host:port
	u, err := url.Parse(h.cfg.EasytierURL)
	if err != nil {
		return false
	}
	host := u.Host
	if host == "" {
		return false
	}
	// TCP 探测：能建立连接即认为 EasyTier RPC Portal 在监听
	conn, err := net.DialTimeout("tcp", host, 2*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// easytierRequest makes an authenticated request to EasyTier Web API.
// 用于可选的实时节点列表查询。EasyTier Web API 不需要 Bearer Token，
// 仅通过监听 127.0.0.1 限制访问。
func (h *Handler) easytierRequest(method, path string) ([]byte, int, error) {
	if h.cfg.EasytierURL == "" {
		return nil, 0, fmt.Errorf("easytier not configured")
	}

	req, err := http.NewRequest(method, h.cfg.EasytierURL+path, nil)
	if err != nil {
		return nil, 0, err
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()

	// 读取响应体（使用 io.ReadAll 等价逻辑）
	var buf [4096]byte
	var body []byte
	for {
		n, readErr := resp.Body.Read(buf[:])
		if n > 0 {
			body = append(body, buf[:n]...)
		}
		if readErr != nil {
			break
		}
	}
	return body, resp.StatusCode, nil
}

// GetStatus returns virtual network status
//
// 响应字段说明：
//   - enabled: EasyTier 服务是否可用（服务端配置 + 健康检查通过）
//   - status: 当前用户在此 Space 的连接状态（connected/disconnected）
//   - serverAddr: EasyTier 公网入口地址（客户端用 -p 参数连接）
//   - networkName: EasyTier 网络名称（客户端用 --network-name 参数）
//   - hasSecret: 服务端是否持有 network_secret（实际 secret 通过 Connect 接口下发）
//   - nodes: 当前 Space 内活跃节点数（数据库记录，非实时）
//   - ip: 当前用户的虚拟 IP（仅在已连接时返回）
func (h *Handler) GetStatus(c *gin.Context) {
	spaceID, err := middleware.RequireSpaceID(c)
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	if err := h.requireSpaceMembership(c, spaceID); err != nil {
		errors.JSONError(c, err)
		return
	}
	userID := middleware.GetUserID(c)

	// Check if EasyTier is available
	easytierAvailable := h.isEasytierAvailable()

	// Check user's current session
	var session model.VirtualNetSession
	hasSession := h.db.Where("space_id = ? AND user_id = ? AND status = ?", spaceID, userID, "connected").First(&session).Error == nil

	// Get online nodes count (database records)
	var nodeCount int64
	h.db.Model(&model.VirtualNetNode{}).Where("space_id = ? AND status = ?", spaceID, "active").Count(&nodeCount)

	// 构造 EasyTier 公网入口地址（供客户端 -p 参数使用）
	// 从 PublicAddress 中提取纯 host（IP 或域名），拼接 udp://<host>:<port>
	serverAddr := ""
	if host := extractHost(h.cfg.PublicAddress); host != "" && h.cfg.EasytierPort > 0 {
		serverAddr = fmt.Sprintf("udp://%s:%d", host, h.cfg.EasytierPort)
	}

	errors.Success(c, gin.H{
		"enabled":      easytierAvailable,
		"status":       map[bool]string{true: "connected", false: "disconnected"}[hasSession],
		"serverAddr":   serverAddr,
		"networkName":  "ridgericetalk", // 固定网络名，所有 Space 共享同一 EasyTier 网络
		"hasSecret":    h.cfg.EasytierSecret != "",
		"easytierPort": h.cfg.EasytierPort,
		"nodes":        nodeCount,
		"ip": func() string {
			if hasSession {
				return session.IP
			}
			return ""
		}(),
	})
}

// Connect joins the virtual network
//
// DES-2026-0731-02: EasyTier 模式下，服务端不再调用 Headscale API 注册节点。
// 服务端仅做：
//   1. 校验请求体（可选 networkName 参数，默认 "ridgericetalk"）
//   2. 检查 EasyTier 服务可用性
//   3. 创建数据库会话记录
//   4. 下发 network_name + network_secret 给客户端
//
// 客户端收到响应后，在本地启动 easytier-core 子进程加入网络。
// 子进程命令示例：
//   easytier-core -i 0.0.0.0 --network-name ridgericetalk \
//     --network-secret <secret> -p udp://<server>:5007 \
//     --hostname rrt-<username> --mtu 1380
func (h *Handler) Connect(c *gin.Context) {
	spaceID, err := middleware.RequireSpaceID(c)
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	if err := h.requireSpaceMembership(c, spaceID); err != nil {
		errors.JSONError(c, err)
		return
	}
	userID := middleware.GetUserID(c)

	// 接受可选的 networkName 参数（默认 "ridgericetalk"）
	// 兼容旧的 networkCidr 字段（已废弃，仅日志记录）
	var body struct {
		NetworkName string `json:"networkName"`
		NetworkCIDR string `json:"networkCidr"` // 已废弃，保留解析以向后兼容
	}
	// SEC-001: 允许空请求体（NetworkName 可选），但拒绝非法 JSON
	if err := httpbind.BindJSONAllowEmpty(c, &body); err != nil {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("invalid JSON body: "+err.Error()))
		return
	}

	// 默认网络名称
	if body.NetworkName == "" {
		body.NetworkName = "ridgericetalk"
	}

	// Check if already connected in this space
	var existing model.VirtualNetSession
	if err := h.db.Where("space_id = ? AND user_id = ? AND status = ?", spaceID, userID, "connected").First(&existing).Error; err == nil {
		errors.JSONError(c, errors.New(errors.VIRTUALNET_ALREADY_CONNECTED, "already connected to virtual network"))
		return
	}

	// Check if EasyTier is available
	if !h.isEasytierAvailable() {
		errors.JSONError(c, errors.New(errors.VIRTUALNET_SERVICE_UNAVAILABLE,
			"virtual network service is not available. EasyTier is not configured or unreachable."))
		return
	}

	// 构造节点名称（客户端 easytier-core --hostname 参数使用）。
	// username 已退役，改用 userId——ASCII 安全、全局唯一、非展示字段。
	nodeName := fmt.Sprintf("rrt-%s", userID)

	// 构造 EasyTier 公网入口地址
	serverAddr := ""
	if host := extractHost(h.cfg.PublicAddress); host != "" && h.cfg.EasytierPort > 0 {
		serverAddr = fmt.Sprintf("udp://%s:%d", host, h.cfg.EasytierPort)
	}

	// 创建会话记录
	// IP 字段在 EasyTier 模式下由客户端连接后通过 UpdateNodeIP 接口回填
	// （当前版本暂不实现回填，IP 留空，由客户端自行查询虚拟网卡 IP）
	session := &model.VirtualNetSession{
		ID:          idgen.GenerateID(idgen.PrefixNet),
		SpaceID:     spaceID,
		UserID:      userID,
		NodeID:      nodeName, // EasyTier 模式下 NodeID 即节点名称
		Status:      "connected",
		IP:          "", // 客户端连接后由 EasyTier 自动分配，服务端不感知
		NetworkName: body.NetworkName,
	}
	if err := h.db.Create(session).Error; err != nil {
		errors.JSONError(c, errors.ErrInternal.WithDetails("failed to create session"))
		return
	}

	// 创建节点记录
	node := &model.VirtualNetNode{
		ID:        idgen.GenerateID(idgen.PrefixNet),
		SpaceID:   spaceID,
		SessionID: session.ID,
		Name:      nodeName,
		IP:        "", // 由客户端连接后回填（当前版本不实现）
		Status:    "active",
	}
	h.db.Create(node)

	// C37: Broadcast virtualnet_user_connected event to all clients
	if h.hub != nil {
		data, _ := json.Marshal(map[string]interface{}{
			"type": "virtualnet_user_connected",
			"payload": map[string]interface{}{
				"userId":      userID,
				"nodeId":      nodeName,
				"nodeName":    nodeName,
				"networkName": body.NetworkName,
				"sessionId":   session.ID,
			},
		})
		h.hub.Broadcast(data)
	}

	// 下发 EasyTier 连接凭证给客户端
	// 客户端使用这些参数启动 easytier-core 子进程
	errors.Success(c, gin.H{
		"networkName":   body.NetworkName,
		"networkSecret": h.cfg.EasytierSecret, // 服务端生成的网络密钥，所有客户端共享
		"serverAddr":    serverAddr,           // EasyTier 公网入口（udp://<ip>:<port>）
		"nodeName":      nodeName,             // 客户端 --hostname 参数
		"sessionId":     session.ID,
		"mtu":           1380, // EasyTier 默认 MTU，适配大多数 NAT 环境
	})
}

// Disconnect leaves the virtual network
//
// DES-2026-0731-02: EasyTier 模式下，服务端不再调用 Headscale API 删除节点。
// 服务端仅更新数据库会话状态。客户端子进程的停止由客户端本地 IPC 处理。
func (h *Handler) Disconnect(c *gin.Context) {
	spaceID, err := middleware.RequireSpaceID(c)
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	if err := h.requireSpaceMembership(c, spaceID); err != nil {
		errors.JSONError(c, err)
		return
	}
	userID := middleware.GetUserID(c)

	// Find active session in the current space
	var session model.VirtualNetSession
	if err := h.db.Where("space_id = ? AND user_id = ? AND status = ?", spaceID, userID, "connected").First(&session).Error; err != nil {
		errors.JSONError(c, errors.New(errors.VIRTUALNET_NOT_CONNECTED, "not connected to virtual network"))
		return
	}

	// Update session status
	h.db.Model(&session).Update("status", "disconnected")

	// Update node status
	h.db.Model(&model.VirtualNetNode{}).Where("session_id = ?", session.ID).Update("status", "inactive")

	// C37: Broadcast virtualnet_user_disconnected event to all clients
	if h.hub != nil {
		data, _ := json.Marshal(map[string]interface{}{
			"type": "virtualnet_user_disconnected",
			"payload": map[string]interface{}{
				"userId":   userID,
				"nodeId":   session.NodeID,
				"ip":       session.IP,
				"nodeName": session.NodeID,
			},
		})
		h.hub.Broadcast(data)
	}

	errors.Success(c, gin.H{"message": "disconnected from virtual network"})
}

// resolveNodeDisplayNames 将节点名（rrt-<userId>）批量解析为显示名（displayName）。
// username 已退役：节点列表展示用显示名，读时实时解析（与 minigames lookupDisplayName 同语义）。
// 返回 hostname → displayName 映射；未知 userId（如 rrt-server 等非用户节点）不进入映射，
// 由调用方回退为原始 hostname。
func resolveNodeDisplayNames(db *gorm.DB, nodes []model.VirtualNetNode) map[string]string {
	displayByName := make(map[string]string, len(nodes))
	var userIDs []string
	for _, n := range nodes {
		if strings.HasPrefix(n.Name, "rrt-") {
			userIDs = append(userIDs, strings.TrimPrefix(n.Name, "rrt-"))
		}
	}
	if len(userIDs) == 0 {
		return displayByName
	}
	var users []model.User
	if err := db.Select("id", "display_name", "username").Where("id IN ?", userIDs).Find(&users).Error; err != nil {
		return displayByName
	}
	for _, u := range users {
		displayName := u.DisplayName
		if displayName == "" {
			displayName = u.Username
		}
		displayByName["rrt-"+u.ID] = displayName
	}
	return displayByName
}

// GetNodes returns the list of virtual network nodes
//
// DES-2026-0731-02: EasyTier 模式下，节点列表来自两个来源：
//   1. 数据库记录（space 内已连接的客户端）
//   2. 可选：EasyTier Web API /api/v1/network/nodes（实时节点状态）
//
// 当前版本仅返回数据库记录，实时 EasyTier 节点信息作为 future enhancement。
//
// name 为显示名（displayName，读时解析），hostname 为 EasyTier 节点名（rrt-<userId>，
// 客户端用于与本地 peer 链路质量 join）。
func (h *Handler) GetNodes(c *gin.Context) {
	spaceID, err := middleware.RequireSpaceID(c)
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	if err := h.requireSpaceMembership(c, spaceID); err != nil {
		errors.JSONError(c, err)
		return
	}
	userID := middleware.GetUserID(c)

	// Check if user is connected in this space
	var session model.VirtualNetSession
	if err := h.db.Where("space_id = ? AND user_id = ? AND status = ?", spaceID, userID, "connected").First(&session).Error; err != nil {
		errors.JSONError(c, errors.New(errors.VIRTUALNET_NOT_CONNECTED, "not connected to virtual network"))
		return
	}

	// Get active nodes in the current space
	var nodes []model.VirtualNetNode
	h.db.Where("space_id = ? AND status = ?", spaceID, "active").Find(&nodes)

	// 批量解析显示名（username 已退役，读时解析）
	displayByName := resolveNodeDisplayNames(h.db, nodes)

	type NodeInfo struct {
		ID        string `json:"id"`
		Name      string `json:"name"`     // 显示名（displayName）
		Hostname  string `json:"hostname"` // EasyTier 节点名（rrt-<userId>，供 peer join）
		IP        string `json:"ip"`
		Status    string `json:"status"`
		IsSelf    bool   `json:"isSelf"`
		LastSeen  string `json:"lastSeen,omitempty"`
		LatencyMs int    `json:"latencyMs,omitempty"`
	}

	result := make([]NodeInfo, 0, len(nodes))
	for _, n := range nodes {
		isSelf := n.SessionID == session.ID
		lastSeen := ""
		if n.LastSeenAt != nil {
			lastSeen = n.LastSeenAt.Format(time.RFC3339)
		}
		displayName := n.Name
		if resolved, ok := displayByName[n.Name]; ok {
			displayName = resolved
		}
		result = append(result, NodeInfo{
			ID:        n.ID,
			Name:      displayName,
			Hostname:  n.Name,
			IP:        n.IP,
			Status:    n.Status,
			IsSelf:    isSelf,
			LastSeen:  lastSeen,
			LatencyMs: n.LatencyMs,
		})
	}

	errors.Success(c, result)
}

// ReportIP 客户端上报虚拟 IP
//
// 客户端连接 EasyTier 成功后，通过本地 RPC 查询到分配的虚拟 IP，
// 调用此接口上报给服务端。服务端更新 session 和 node 的 IP 字段，
// 并广播 virtualnet_ip_reported 事件给其他客户端，使节点列表能显示 IP。
func (h *Handler) ReportIP(c *gin.Context) {
	spaceID, err := middleware.RequireSpaceID(c)
	if err != nil {
		errors.JSONError(c, err)
		return
	}
	if err := h.requireSpaceMembership(c, spaceID); err != nil {
		errors.JSONError(c, err)
		return
	}
	userID := middleware.GetUserID(c)

	var body struct {
		IP string `json:"ip" binding:"required"`
	}
	if err := c.ShouldBindJSON(&body); err != nil {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("ip is required"))
		return
	}

	// 验证 IP 格式
	if net.ParseIP(body.IP) == nil {
		errors.JSONError(c, errors.ErrBadRequest.WithDetails("invalid IP format"))
		return
	}

	// 查找当前 space 内该用户的活跃 session
	var session model.VirtualNetSession
	if err := h.db.Where("space_id = ? AND user_id = ? AND status = ?", spaceID, userID, "connected").First(&session).Error; err != nil {
		errors.JSONError(c, errors.New(errors.VIRTUALNET_NOT_CONNECTED, "not connected to virtual network"))
		return
	}

	// 更新 session 的 IP
	if err := h.db.Model(&session).Update("ip", body.IP).Error; err != nil {
		errors.JSONError(c, errors.ErrInternal.WithDetails("failed to update session IP"))
		return
	}

	// 更新关联 node 的 IP
	if err := h.db.Model(&model.VirtualNetNode{}).Where("session_id = ?", session.ID).Update("ip", body.IP).Error; err != nil {
		errors.JSONError(c, errors.ErrInternal.WithDetails("failed to update node IP"))
		return
	}

	// 广播 IP 更新事件给所有客户端
	if h.hub != nil {
		data, _ := json.Marshal(map[string]interface{}{
			"type": "virtualnet_ip_reported",
			"payload": map[string]interface{}{
				"userId":    userID,
				"nodeId":    session.NodeID,
				"ip":        body.IP,
				"sessionId": session.ID,
			},
		})
		h.hub.Broadcast(data)
	}

	errors.Success(c, gin.H{"message": "IP reported successfully"})
}
