package server

import (
	"time"

	"ridgericetalk/internal/admin"
)

// startBackgroundJobs launches long-running maintenance goroutines.
func (a *App) startBackgroundJobs() {
	go a.scheduleHandler.StartReminderLoop(a.backgroundCtx)
	a.minigamesHandler.StartCleanupLoop(a.backgroundCtx)
	// DES-2026-0912-02 §4.3: purge CloudFS trash entries past their retention.
	a.cloudfsHandler.Service().StartTrashCleanupLoop(a.backgroundCtx)
	a.breakGlassProtector.StartCleanup(a.backgroundCtx)
	a.hub.OnDocEditorJoin = a.sharedocHandler.AddEditor
	a.hub.OnDocEditorLeave = a.sharedocHandler.RemoveEditor
	a.hub.AuthorizeRoom = a.minigamesHandler.AuthorizeRoom

	// A11 (DES-20261001-01 §12.2): admin alert evaluator — disk/health/
	// livekit/database/tts_worker checks with 3-cycle debounce, persisted to
	// admin_alerts and broadcast as admin_alert over the admin WS. Lifecycle
	// follows backgroundCtx: cancel stops the ticker goroutine (优雅关闭).
	alertEvaluator := admin.NewAlertEvaluator(admin.AlertEvaluatorConfig{
		DB:      a.db.DB,
		Hub:     a.hub,
		Cfg:     a.cfg,
		Log:     a.log,
		LiveKit: a.livekitMgr,
	})
	alertEvaluator.Start(a.backgroundCtx)

	// C-7: Start audit log retention cleanup (daily, deletes AuditLog older than 180 days)
	// SecurityAuditLog is NOT cleaned (kept permanently for security forensics).
	go func() {
		// Run once at startup
		if err := a.adminSvc.CleanupOldAuditLogs(); err != nil {
			a.log.Warn("failed to cleanup old audit logs", "error", err)
		}
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-a.backgroundCtx.Done():
				return
			case <-ticker.C:
				if err := a.adminSvc.CleanupOldAuditLogs(); err != nil {
					a.log.Warn("failed to cleanup old audit logs", "error", err)
				}
			}
		}
	}()
}

func (a *App) stopBackgroundJobs() {
	if a.backgroundCancel != nil {
		a.backgroundCancel()
	}
}
