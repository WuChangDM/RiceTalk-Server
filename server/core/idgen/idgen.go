package idgen

import (
	"fmt"
	"sync"
	"time"
)

const (
	epoch          = int64(1609459200000) // 2021-01-01 00:00:00 UTC
	workerBits     = uint(5)
	dataCenterBits = uint(5)
	sequenceBits   = uint(12)

	workerMax     = int64(-1) ^ (int64(-1) << workerBits)
	dataCenterMax = int64(-1) ^ (int64(-1) << dataCenterBits)
	sequenceMax   = int64(-1) ^ (int64(-1) << sequenceBits)

	timeShift       = workerBits + dataCenterBits + sequenceBits
	workerShift     = dataCenterBits + sequenceBits
	dataCenterShift = sequenceBits
)

// Snowflake is a distributed ID generator
type Snowflake struct {
	mu            sync.Mutex
	lastTimestamp int64
	workerID      int64
	dataCenterID  int64
	sequence      int64
}

var defaultGen *Snowflake
var once sync.Once

// Init initializes the global Snowflake generator
func Init(workerID, dataCenterID int64) error {
	if workerID < 0 || workerID > workerMax {
		return fmt.Errorf("worker ID must be between 0 and %d", workerMax)
	}
	if dataCenterID < 0 || dataCenterID > dataCenterMax {
		return fmt.Errorf("data center ID must be between 0 and %d", dataCenterMax)
	}
	once.Do(func() {
		defaultGen = &Snowflake{
			lastTimestamp: -1,
			workerID:      workerID,
			dataCenterID:  dataCenterID,
			sequence:      0,
		}
	})
	return nil
}

// Next generates the next unique ID
func Next() int64 {
	defaultGen.mu.Lock()
	defer defaultGen.mu.Unlock()

	now := time.Now().UnixMilli()

	if now < defaultGen.lastTimestamp {
		// Clock moved backwards, wait until it catches up (max 10ms)
		deadline := time.Now().Add(10 * time.Millisecond)
		for now <= defaultGen.lastTimestamp && time.Now().Before(deadline) {
			now = time.Now().UnixMilli()
		}
		if now <= defaultGen.lastTimestamp {
			// Still behind after timeout: prevent duplicate IDs by advancing sequence
			now = defaultGen.lastTimestamp
		}
	}

	if now == defaultGen.lastTimestamp {
		defaultGen.sequence = (defaultGen.sequence + 1) & sequenceMax
		if defaultGen.sequence == 0 {
			// Sequence overflow, wait for next millisecond
			for now <= defaultGen.lastTimestamp {
				now = time.Now().UnixMilli()
			}
		}
	} else {
		defaultGen.sequence = 0
	}

	defaultGen.lastTimestamp = now

	id := ((now - epoch) << timeShift) |
		(defaultGen.dataCenterID << dataCenterShift) |
		(defaultGen.workerID << workerShift) |
		(defaultGen.sequence)

	return id
}

// NextString generates the next ID as a string
func NextString() string {
	return fmt.Sprintf("%d", Next())
}

// String converts an int64 ID to string
func String(id int64) string {
	return fmt.Sprintf("%d", id)
}

// H31: Business-prefixed ID generation (design doc §5.2).
// IDs use the format "<prefix>_<snowflake>" for readability in logs and databases.
// Example: GenerateID("user") -> "user_723169646487932928"

// Business prefix constants for common entity types.
const (
	PrefixUser    = "user"
	PrefixChannel = "ch"
	PrefixMessage = "msg"
	PrefixSpace   = "space"
	PrefixSession    = "sess"
	PrefixBot        = "bot"
	PrefixFile       = "file"
	PrefixDoc        = "doc"
	PrefixComment    = "cmt"
	PrefixStroke     = "stroke"
	PrefixWhiteboard = "wb"
	PrefixEvent      = "event"
	PrefixGame       = "game"
	PrefixNet        = "net"
	PrefixReset      = "reset"
	PrefixDM         = "dm"
	PrefixRemoteAssist = "ra" // T49: 远程协助会话
	PrefixShare        = "share" // DES-2026-0912-05: 云文件分享链接
	PrefixInvite       = "invite" // A8-S1: 日程事件邀请 RSVP
	PrefixAlert        = "alert" // DES-20261001-01 §12: 管理后台监控告警
)

// GenerateID generates a prefixed ID like "user_723169646487932928".
// The prefix should be a short business identifier (see Prefix* constants).
func GenerateID(prefix string) string {
	return prefix + "_" + NextString()
}
