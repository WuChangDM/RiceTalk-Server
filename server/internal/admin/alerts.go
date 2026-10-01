// Package admin — alerts.go
//
// DES-20261001-01 §12「A11 管理后台监控告警」评估器。
//
// 职责：周期性运行五项运维检查（disk/health/livekit/database/tts_worker），
// 对超标项做防抖（连续 3 个周期才 open），把告警生命周期持久化到
// admin_alerts 表，并在状态变化时向 admin WebSocket 广播 admin_alert 事件。
//
// 数据契约（与迁移 000040 / model.AdminAlert 对应）：
//   - 同一 type 至多一行未恢复告警（resolved_at IS NULL）；防抖达标后若已有
//     活跃行则只更新 last_seen/message（update 事件），否则插入新行（open）；
//   - 检查恢复（不超标）立即置 resolved_at（resolved 事件）并清零计数；
//   - muted_until 静默只压制 WS 推送，表照记（审计优先于免打扰）。
//
// 可测性优先：interval / 检查函数 / 时间源 / 广播全部可注入（见
// AlertEvaluatorConfig 与 newTestEvaluator 用法，alerts_test.go）。
package admin

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/bots"
	"ridgericetalk/internal/config"
	applogger "ridgericetalk/internal/logger"
	"ridgericetalk/internal/livekitmgr"
	"ridgericetalk/internal/model"
	"ridgericetalk/internal/realtime"
)

// 告警类型与严重级别取值域（DES §12.2 检查表；与迁移 000040 注释一致）。
const (
	AlertTypeDisk     = "disk"
	AlertTypeHealth   = "health"
	AlertTypeLiveKit  = "livekit"
	AlertTypeDatabase = "database"
	AlertTypeTTSWork  = "tts_worker"

	AlertSeverityWarn  = "warn"
	AlertSeverityAlert = "alert"
)

// WS 事件类型（isRelevant 白名单与前端 App.tsx 的处理一一对应）。
const (
	WSEventAdminAlert         = "admin_alert"
	WSEventAdminAlertSnapshot = "admin_alert_snapshot"
)

const (
	// DefaultAlertInterval 生产评估周期：60s（DES §12.2）。
	DefaultAlertInterval = 60 * time.Second
	// DefaultDebounceThreshold 防抖阈值：连续 3 个周期超标才 open。
	// 单次抖动（如瞬时高负载导致的 health 超时）不产生告警噪音。
	DefaultDebounceThreshold = 3
	// DiskUsageThresholdPercent 磁盘用量告警阈值（DES §12.2：>85% → alert）。
	DiskUsageThresholdPercent = 85.0
	// alertProbeTimeout 单项网络探测（health HTTP / livekit TCP / db Ping）超时。
	alertProbeTimeout = 5 * time.Second
)

// CheckOutcome 是单项检查的一周期结论。
// Firing=false 时 Severity/Message 无意义（视为「本周期正常」）。
type CheckOutcome struct {
	Firing   bool
	Severity string // AlertSeverityWarn | AlertSeverityAlert
	Message  string
}

// AlertCheckFunc 是可注入的单项检查函数。
type AlertCheckFunc func(ctx context.Context) CheckOutcome

// AlertBroadcaster 是可注入的状态变化广播钩子（生产实现走 hub.Broadcast）。
// action 取值："open" | "update" | "resolved"。
type AlertBroadcaster func(action string, alert *model.AdminAlert)

// AlertEvaluatorConfig 汇总评估器依赖；零值字段使用生产默认。
type AlertEvaluatorConfig struct {
	DB     *gorm.DB
	Hub    *realtime.Hub
	Cfg    *config.Config
	Log    *applogger.Logger
	LiveKit *livekitmgr.Manager // 可为 nil（未托管 LiveKit 的部署），届时跳过 livekit 检查

	// Interval 评估周期，默认 60s。
	Interval time.Duration
	// DebounceThreshold 连续超标多少个周期才 open，默认 3。
	DebounceThreshold int
	// Checks 覆盖默认检查表（测试注入假检查用）；nil 时用 DefaultAlertChecks。
	Checks map[string]AlertCheckFunc
	// Now 时间源，默认 time.Now。
	Now func() time.Time
	// Broadcaster 覆盖默认 hub 广播（测试断言用）。
	Broadcaster AlertBroadcaster
}

// AlertEvaluator 周期评估运维检查并把告警状态落库 + 广播。
// 生命周期随主进程：Start(ctx) 后 goroutine 运行，ctx 取消即优雅退出。
type AlertEvaluator struct {
	db                *gorm.DB
	hub               *realtime.Hub
	log               *applogger.Logger
	interval          time.Duration
	debounceThreshold int
	checks            map[string]AlertCheckFunc
	nowFn             func() time.Time
	broadcastFn       AlertBroadcaster

	// mu 保护 streaks（Run goroutine 独写；外部测试读）。
	mu      sync.Mutex
	streaks map[string]int
}

// NewAlertEvaluator 构造评估器（不启动；调用 Start）。
func NewAlertEvaluator(cfg AlertEvaluatorConfig) *AlertEvaluator {
	if cfg.Interval <= 0 {
		cfg.Interval = DefaultAlertInterval
	}
	if cfg.DebounceThreshold <= 0 {
		cfg.DebounceThreshold = DefaultDebounceThreshold
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	checks := cfg.Checks
	if checks == nil {
		checks = DefaultAlertChecks(cfg.Cfg, cfg.DB, cfg.LiveKit)
	}
	return &AlertEvaluator{
		db:                cfg.DB,
		hub:               cfg.Hub,
		log:               cfg.Log,
		interval:          cfg.Interval,
		debounceThreshold: cfg.DebounceThreshold,
		checks:            checks,
		nowFn:             cfg.Now,
		broadcastFn:       cfg.Broadcaster,
		streaks:           make(map[string]int),
	}
}

// Start 启动评估循环：立即跑首轮，然后按 interval 周期执行；
// ctx 取消后当前周期结束即退出（优雅关闭，无泄漏 goroutine）。
func (e *AlertEvaluator) Start(ctx context.Context) {
	go e.run(ctx)
}

func (e *AlertEvaluator) run(ctx context.Context) {
	ticker := time.NewTicker(e.interval)
	defer ticker.Stop()
	e.evaluateAll(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.evaluateAll(ctx)
		}
	}
}

// evaluateAll 跑一轮全部检查。按 type 名排序执行，保证测试与日志顺序确定。
func (e *AlertEvaluator) evaluateAll(ctx context.Context) {
	types := make([]string, 0, len(e.checks))
	for t := range e.checks {
		types = append(types, t)
	}
	sort.Strings(types)
	for _, t := range types {
		select {
		case <-ctx.Done():
			return
		default:
		}
		outcome := e.checks[t](ctx)
		e.record(t, outcome)
	}
}

// streak 返回某 type 当前连续超标周期数（测试读取用）。
func (e *AlertEvaluator) streak(alertType string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.streaks[alertType]
}

// record 消化单项检查结果：防抖计数、open/update/resolved 状态迁移。
// 所有 DB 操作幂等且并发安全（同 type 活跃行唯一由「先查后插 + 评估器单
// goroutine」保证；生产只有一个 Evaluator 实例）。
func (e *AlertEvaluator) record(alertType string, outcome CheckOutcome) {
	now := e.nowFn()
	if outcome.Firing {
		e.mu.Lock()
		e.streaks[alertType]++
		streak := e.streaks[alertType]
		e.mu.Unlock()
		if streak < e.debounceThreshold {
			return // 未达防抖阈值：静默计数，不产生任何状态
		}
		e.ensureOpen(alertType, outcome, now)
		return
	}

	// 本周期正常：清零计数；若存在活跃行则立即恢复。
	e.mu.Lock()
	e.streaks[alertType] = 0
	e.mu.Unlock()

	var row model.AdminAlert
	err := e.db.Where("type = ? AND resolved_at IS NULL", alertType).First(&row).Error
	if err != nil {
		return // 本来就没有活跃告警
	}
	row.ResolvedAt = &now
	if err := e.db.Save(&row).Error; err != nil {
		e.log.Error("failed to resolve admin alert", "type", alertType, "error", err)
		return
	}
	e.log.Info("admin alert resolved", "type", alertType, "id", row.ID)
	e.emit("resolved", &row)
}

// ensureOpen 达到防抖阈值后调用：复用同 type 活跃行（update）或插入新行（open）。
func (e *AlertEvaluator) ensureOpen(alertType string, outcome CheckOutcome, now time.Time) {
	var row model.AdminAlert
	err := e.db.Where("type = ? AND resolved_at IS NULL", alertType).First(&row).Error
	switch {
	case err == nil:
		// 同类未 resolve 不重复插入：刷新 last_seen/message/severity。
		row.LastSeenAt = now
		row.Message = outcome.Message
		row.Severity = outcome.Severity
		if err := e.db.Save(&row).Error; err != nil {
			e.log.Error("failed to update admin alert", "type", alertType, "error", err)
			return
		}
		e.emit("update", &row)
	case err == gorm.ErrRecordNotFound:
		row = model.AdminAlert{
			ID:         idgen.GenerateID(idgen.PrefixAlert),
			Type:       alertType,
			Severity:   outcome.Severity,
			Message:    outcome.Message,
			FirstSeenAt: now,
			LastSeenAt:  now,
		}
		if err := e.db.Create(&row).Error; err != nil {
			e.log.Error("failed to create admin alert", "type", alertType, "error", err)
			return
		}
		e.log.Warn("admin alert opened", "type", alertType, "id", row.ID, "message", outcome.Message)
		e.emit("open", &row)
	default:
		e.log.Error("failed to query admin alert", "type", alertType, "error", err)
	}
}

// emit 广播状态变化。静默检查按 type 生效：该 type 任一行 muted_until 未过期
// （实践中即活跃行上的静默，POST /admin/alerts/:id/mute 写入）则不推 WS。
func (e *AlertEvaluator) emit(action string, row *model.AdminAlert) {
	if e.broadcastFn != nil {
		e.broadcastFn(action, row)
		return
	}
	if e.hub == nil {
		return
	}
	if AlertTypeMuted(e.db, row.Type, e.nowFn()) {
		e.log.Info("admin alert suppressed by mute window", "type", row.Type, "action", action)
		return
	}
	data, err := json.Marshal(map[string]interface{}{
		"type":    WSEventAdminAlert,
		"payload": AlertEventPayload(action, row),
	})
	if err != nil {
		return
	}
	e.hub.Broadcast(data)
}

// AlertEventPayload 构造 WS payload（REST/快照/事件三处共用同一字段契约）。
func AlertEventPayload(action string, row *model.AdminAlert) map[string]interface{} {
	return map[string]interface{}{
		"id":          row.ID,
		"type":        row.Type,
		"severity":    row.Severity,
		"message":     row.Message,
		"firstSeenAt": formatAlertTime(row.FirstSeenAt),
		"lastSeenAt":  formatAlertTime(row.LastSeenAt),
		"resolvedAt":  formatAlertTimePtr(row.ResolvedAt),
		"mutedUntil":  formatAlertTimePtr(row.MutedUntil),
		"action":      action,
	}
}

func formatAlertTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339)
}

func formatAlertTimePtr(t *time.Time) interface{} {
	if t == nil {
		return nil
	}
	return formatAlertTime(*t)
}

// AlertTypeMuted 报告该 type 当前是否处于静默窗口（任一行 muted_until 未过期）。
// 静默按 type 而非按行生效：即使告警恢复后重新 open（新行），旧行的静默
// 仍然压制同 type 的推送——「静默 24h」的语义是「这个类型别再烦我」。
// 逐行比较而非 SQL MAX()：SQLite/PG 的时间聚合 Scan 行为差异大，逐行最稳。
func AlertTypeMuted(db *gorm.DB, alertType string, now time.Time) bool {
	var rows []model.AdminAlert
	if err := db.Select("muted_until").
		Where("type = ? AND muted_until IS NOT NULL", alertType).
		Find(&rows).Error; err != nil {
		return false
	}
	for _, r := range rows {
		if r.MutedUntil != nil && now.Before(*r.MutedUntil) {
			return true
		}
	}
	return false
}

// DefaultAlertChecks 构造生产检查表（DES §12.2 表格的实现）。
//
// 数据来源（均为既有生产途径，无新增依赖）：
//   - disk:      getDiskUsageStats()（admin/sysinfo.go，gopsutil，与
//                GET /api/admin/system/usage 同源，根分区）；
//   - health:    HTTP GET 127.0.0.1:{APIPort}/api/health（legacy 路由组）；
//   - livekit:   manager.InGraceWindow()（N14 宽限窗，窗内跳过）+ TCP dial
//                127.0.0.1:{LiveKitPort}（默认 7880）；
//   - database:  gorm 句柄底层 *sql.DB PingContext；
//   - tts_worker: bots.TTSStatus()（globalTTSWorker 单例状态轮询；N16 管理器
//                未导出崩溃事件回调，见 classifyTTSStatus 的取舍说明）。
func DefaultAlertChecks(cfg *config.Config, db *gorm.DB, lk *livekitmgr.Manager) map[string]AlertCheckFunc {
	return map[string]AlertCheckFunc{
		AlertTypeDisk: diskAlertCheck,
		AlertTypeHealth: func(context.Context) CheckOutcome {
			return healthAlertCheck(cfg)
		},
		AlertTypeLiveKit: func(context.Context) CheckOutcome {
			return livekitAlertCheck(cfg, lk)
		},
		AlertTypeDatabase: func(ctx context.Context) CheckOutcome {
			return databaseAlertCheck(db, ctx)
		},
		AlertTypeTTSWork: func(context.Context) CheckOutcome {
			return ttsWorkerAlertCheck(bots.TTSStatus)
		},
	}
}

// diskAlertCheck 磁盘用量 >85% → alert。读不到用量（gopsutil 失败）不告警：
// 「未知」与「故障」不同，避免评估器自身故障制造告警风暴。
func diskAlertCheck(context.Context) CheckOutcome {
	u := getDiskUsageStats()
	if u == nil {
		return CheckOutcome{}
	}
	if u.UsedPercent > DiskUsageThresholdPercent {
		return CheckOutcome{
			Firing:   true,
			Severity: AlertSeverityAlert,
			Message: fmt.Sprintf("磁盘用量 %.1f%%（阈值 %.0f%%），路径 %s",
				u.UsedPercent, DiskUsageThresholdPercent, u.Path),
		}
	}
	return CheckOutcome{}
}

// healthAlertCheck 本机 API 健康检查失败 → alert。
func healthAlertCheck(cfg *config.Config) CheckOutcome {
	if cfg == nil {
		return CheckOutcome{}
	}
	url := fmt.Sprintf("http://127.0.0.1:%d/api/health", cfg.Port)
	client := &http.Client{Timeout: alertProbeTimeout}
	resp, err := client.Get(url)
	if err != nil {
		return CheckOutcome{
			Firing:   true,
			Severity: AlertSeverityAlert,
			Message:  "本机健康检查失败（API 无响应）: " + err.Error(),
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return CheckOutcome{
			Firing:   true,
			Severity: AlertSeverityAlert,
			Message:  fmt.Sprintf("本机健康检查返回 %d（期望 200）", resp.StatusCode),
		}
	}
	return CheckOutcome{}
}

// livekitAlertCheck LiveKit 信令端口不可达 → alert。
// N14 启动宽限窗内（InGraceWindow）探活失败是预期行为，跳过不告警。
// manager 为 nil（未托管 LiveKit 的部署形态）时同样跳过。
func livekitAlertCheck(cfg *config.Config, lk *livekitmgr.Manager) CheckOutcome {
	if lk == nil {
		return CheckOutcome{}
	}
	if lk.InGraceWindow() {
		return CheckOutcome{}
	}
	port := cfg.LiveKitPort
	if port == 0 {
		port = 7880
	}
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	conn, err := net.DialTimeout("tcp", addr, alertProbeTimeout)
	if err == nil {
		_ = conn.Close()
		return CheckOutcome{}
	}
	hint := "端口不可达"
	if !lk.Running() {
		hint = "托管子进程未运行（等待自动重启或二进制缺失）"
	}
	return CheckOutcome{
		Firing:   true,
		Severity: AlertSeverityAlert,
		Message:  fmt.Sprintf("LiveKit 信令端口 %s 探测失败（%s）: %v", addr, hint, err),
	}
}

// databaseAlertCheck 数据库 Ping 失败 → alert。
func databaseAlertCheck(db *gorm.DB, ctx context.Context) CheckOutcome {
	if db == nil {
		return CheckOutcome{}
	}
	sqlDB, err := db.DB()
	if err != nil {
		return firingDatabaseCheck(err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, alertProbeTimeout)
	defer cancel()
	if err := sqlDB.PingContext(pingCtx); err != nil {
		return firingDatabaseCheck(err)
	}
	return CheckOutcome{}
}

func firingDatabaseCheck(err error) CheckOutcome {
	return CheckOutcome{
		Firing:   true,
		Severity: AlertSeverityAlert,
		Message:  "数据库连接失败: " + err.Error(),
	}
}

// ttsWorkerAlertCheck TTS worker 状态异常 → warn。
//
// statusFn 返回 (ready, modelDir, errText)，生产传入 bots.TTSStatus（N16 全局
// 管理器的状态查询口）。N16 管理器没有导出崩溃/退避事件回调，这里轮询状态
// 并按 errText 分类（真实取舍：轮询有最长一个评估周期的滞后，换来的零侵入）。
// 分类采用「正常降级白名单」而非「崩溃黑名单」：已知的环境未配置/主动关停
// 文案不告警（否则未装 TTS 模型的部署会永挂一条假告警），其余（含未知文本）
// 一律 warn 保守报告——宁可多报不漏报。
func ttsWorkerAlertCheck(statusFn func() (bool, string, string)) CheckOutcome {
	ready, _, errText := statusFn()
	if ready {
		return CheckOutcome{}
	}
	if firing, msg := classifyTTSStatus(errText); firing {
		return CheckOutcome{Firing: true, Severity: AlertSeverityWarn, Message: msg}
	}
	return CheckOutcome{}
}

// ttsBenignErrKeywords 是「正常降级/未配置」类 errText 关键词：TTS 功能本来
// 就没启用（模型缺失、worker 二进制未部署、主动 Close），不算运维故障。
var ttsBenignErrKeywords = []string{
	"model not found",    // 预检失败：模型目录缺失/文件过小
	"binary not found",   // worker 二进制未部署
	"not initialized",    // 从未初始化（含 "engine not initialized" 尾缀）
	"manager closed",     // 主动关停
}

func classifyTTSStatus(errText string) (firing bool, message string) {
	if errText == "" {
		return false, ""
	}
	// 顺序敏感：崩溃类文本尾部也带 "engine not initialized" 家族后缀
	//（onExit/markSpawnFailed 的消息模板），必须先判崩溃关键词。
	crashKeywords := []string{"exited unexpectedly", "failed to start", "pipe failed"}
	for _, kw := range crashKeywords {
		if containsFold(errText, kw) {
			return true, "TTS worker 异常: " + errText
		}
	}
	for _, kw := range ttsBenignErrKeywords {
		if containsFold(errText, kw) {
			return false, ""
		}
	}
	// 未知文本：保守告警。
	return true, "TTS worker 状态异常: " + errText
}

func containsFold(s, substr string) bool {
	return len(s) >= len(substr) && strings.Contains(strings.ToLower(s), strings.ToLower(substr))
}
