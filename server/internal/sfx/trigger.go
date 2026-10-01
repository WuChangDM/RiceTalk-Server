package sfx

import (
	"log"

	"gorm.io/gorm"

	"ridgericetalk/internal/model"
)

// TriggerJoin plays the user's entrance sound into the room (best-effort).
func (s *Service) TriggerJoin(userID, roomID string) { s.trigger(userID, roomID, "join") }

// TriggerLeave plays the user's exit sound into the room (best-effort).
func (s *Service) TriggerLeave(userID, roomID string) { s.trigger(userID, roomID, "leave") }

// trigger resolves the user's binding and plays the sound fire-and-forget. All guards
// live here so the voice module's call sites stay trivial.
//
// NOTE: no time-window debounce. Duplicate suppression is handled by the caller's DB
// state: JoinVoice/webhook only call TriggerJoin when a participant row is actually
// created, and LeaveVoice/webhook only call TriggerLeave when a row is actually
// soft-deleted (RowsAffected>0). A time window would additionally swallow genuine
// rapid leave→rejoin / rejoin→leave (channel switching), which Discord/KOOK/TeamSpeak
// all play — every real transition must sound.
func (s *Service) trigger(userID, roomID, kind string) {
	if userID == "" || roomID == "" {
		return
	}

	st, err := s.GetSettings(userID)
	if err != nil || st == nil || !st.Enabled {
		return
	}

	var soundID *string
	volume := 100
	if kind == "join" {
		soundID = st.JoinSoundID
		volume = st.JoinVolume
	} else {
		soundID = st.LeaveSoundID
		volume = st.LeaveVolume
	}
	if soundID == nil || *soundID == "" {
		return
	}

	// Reload the sound by ID so a soft-deleted sound is skipped.
	var sound model.UserSound
	if dbErr := s.db.Where("id = ?", *soundID).First(&sound).Error; dbErr != nil {
		if dbErr != gorm.ErrRecordNotFound {
			log.Printf("[sfx] load sound %s failed: %v", *soundID, dbErr)
		}
		return
	}

	absPath := s.absPath(sound.FilePath)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[sfx] play panic (user=%s room=%s kind=%s): %v", userID, roomID, kind, r)
			}
		}()
		if err := s.player.PlayOnce(roomID, absPath, volume); err != nil {
			// Best-effort: a playback failure (e.g. nocgo build, LiveKit unreachable)
			// must never fail the join/leave. Log and move on.
			log.Printf("[sfx] play skipped (user=%s room=%s kind=%s): %v", userID, roomID, kind, err)
		}
	}()
}
