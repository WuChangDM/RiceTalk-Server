package database

import (
	"fmt"

	"gorm.io/gorm"

	"ridgericetalk/core/idgen"
	"ridgericetalk/internal/logger"
	"ridgericetalk/internal/model"
)

// migrateWhiteboardIndependence migrates legacy WhiteboardStroke.ChannelID data
// to the new Whiteboard / WhiteboardStroke.WhiteboardID model.
// It is idempotent: safe to run multiple times.
func migrateWhiteboardIndependence(db *gorm.DB, log *logger.Logger) error {
	// Ensure the whiteboards table exists (AutoMigrate should already have created it).
	if !db.Migrator().HasTable(&model.Whiteboard{}) {
		return nil
	}

	// 1. Detect legacy channel_id column. GORM does not drop removed columns,
	//    so if data was created before this migration the column still exists.
	hasChannelID := db.Migrator().HasColumn(&model.WhiteboardStroke{}, "channel_id")
	if !hasChannelID {
		// Nothing to migrate; just ensure at least one default whiteboard exists.
		return ensureDefaultWhiteboard(db)
	}

	// 2. Find distinct legacy channel IDs that still have strokes.
	var rows []struct {
		ChannelID string
		SpaceID   string
		Count     int64
	}
	if err := db.Raw(`
		SELECT ws.channel_id AS channel_id, c.space_id AS space_id, COUNT(*) AS count
		FROM whiteboard_strokes ws
		JOIN channels c ON c.id = ws.channel_id
		WHERE ws.channel_id IS NOT NULL AND ws.channel_id != ''
		GROUP BY ws.channel_id, c.space_id
	`).Scan(&rows).Error; err != nil {
		log.Warn("failed to query legacy whiteboard strokes", "error", err)
		return ensureDefaultWhiteboard(db)
	}

	spaceID := ""
	for _, r := range rows {
		spaceID = r.SpaceID
		var wb model.Whiteboard
		// Try to find an existing mapping created by a previous run.
		if err := db.Where("name = ? AND space_id = ?", r.ChannelID, r.SpaceID).First(&wb).Error; err != nil {
			wb = model.Whiteboard{
				ID:        idgen.GenerateID(idgen.PrefixWhiteboard),
				SpaceID:   r.SpaceID,
				Name:      r.ChannelID,
				Archived:  false,
				CreatedBy: "system_migration",
			}
			if err := db.Create(&wb).Error; err != nil {
				log.Warn("failed to create whiteboard for legacy channel", "channel_id", r.ChannelID, "error", err)
				continue
			}
		}
		if err := db.Exec(
			"UPDATE whiteboard_strokes SET whiteboard_id = ? WHERE channel_id = ? AND (whiteboard_id IS NULL OR whiteboard_id = '')",
			wb.ID, r.ChannelID,
		).Error; err != nil {
			log.Warn("failed to migrate strokes for legacy channel", "channel_id", r.ChannelID, "error", err)
		}
	}

	// 3. Ensure every space has at least one default whiteboard.
	if err := ensureDefaultWhiteboard(db); err != nil {
		return err
	}

	// 4. If all strokes are migrated, drop the legacy column (optional, SQLite may not support).
	var unmigrated int64
	db.Raw("SELECT COUNT(*) FROM whiteboard_strokes WHERE channel_id IS NOT NULL AND channel_id != '' AND (whiteboard_id IS NULL OR whiteboard_id = '')").Scan(&unmigrated)
	if unmigrated == 0 {
		if err := db.Migrator().DropColumn(&model.WhiteboardStroke{}, "channel_id"); err != nil {
			log.Warn("failed to drop legacy channel_id column from whiteboard_strokes", "error", err)
		}
	}

	if spaceID != "" {
		log.Info("whiteboard independence migration completed", "migrated_boards", len(rows))
	}
	return nil
}

// ensureDefaultWhiteboard creates a default whiteboard for any space that has none.
func ensureDefaultWhiteboard(db *gorm.DB) error {
	var spaces []model.Space
	if err := db.Find(&spaces).Error; err != nil {
		return fmt.Errorf("query spaces for default whiteboard: %w", err)
	}
	for _, sp := range spaces {
		var count int64
		if err := db.Model(&model.Whiteboard{}).Where("space_id = ?", sp.ID).Count(&count).Error; err != nil {
			continue
		}
		if count > 0 {
			continue
		}
		wb := model.Whiteboard{
			ID:        idgen.GenerateID(idgen.PrefixWhiteboard),
			SpaceID:   sp.ID,
			Name:      "默认白板",
			Archived:  false,
			CreatedBy: "system",
		}
		if err := db.Create(&wb).Error; err != nil {
			return fmt.Errorf("create default whiteboard for space %s: %w", sp.ID, err)
		}
	}
	return nil
}
