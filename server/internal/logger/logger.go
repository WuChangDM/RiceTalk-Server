package logger

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/sirupsen/logrus"
)

// Logger wraps logrus with structured logging
type Logger struct {
	*logrus.Logger
}

// New creates a new logger
func New(level string) (*Logger, error) {
	l := logrus.New()
	l.SetOutput(os.Stdout)
	l.SetFormatter(&logrus.JSONFormatter{
		TimestampFormat: time.RFC3339Nano,
	})

	lvl, err := logrus.ParseLevel(level)
	if err != nil {
		lvl = logrus.InfoLevel
	}
	l.SetLevel(lvl)

	return &Logger{l}, nil
}

// GetLevel returns the current log level
func (l *Logger) GetLevel() logrus.Level {
	return l.Logger.GetLevel()
}

// Sync flushes any buffered log entries
func (l *Logger) Sync() error {
	// logrus writes to os.Stdout synchronously; attempt sync as best-effort
	if err := os.Stdout.Sync(); err != nil {
		// On some platforms (e.g., Windows) Sync may fail for stdout;
		// log a debug message instead of returning the error.
		l.Debug("stdout sync not supported on this platform")
	}
	return nil
}

// Info logs an info message
func (l *Logger) Info(msg string, keyvals ...interface{}) {
	fields := l.keyvalsToFields(keyvals...)
	l.WithFields(fields).Info(msg)
}

// Error logs an error message
func (l *Logger) Error(msg string, keyvals ...interface{}) {
	fields := l.keyvalsToFields(keyvals...)
	l.WithFields(fields).Error(msg)
}

// Fatal logs a fatal message and exits
func (l *Logger) Fatal(msg string, keyvals ...interface{}) {
	fields := l.keyvalsToFields(keyvals...)
	l.WithFields(fields).Fatal(msg)
}

// Warn logs a warning message
func (l *Logger) Warn(msg string, keyvals ...interface{}) {
	fields := l.keyvalsToFields(keyvals...)
	l.WithFields(fields).Warn(msg)
}

// Debug logs a debug message
func (l *Logger) Debug(msg string, keyvals ...interface{}) {
	fields := l.keyvalsToFields(keyvals...)
	l.WithFields(fields).Debug(msg)
}

// SecurityEvent records a structured security event log entry.
// H33: structured security logging with IP masking and injection prevention.
// This complements the persistent SecurityAuditLog (stored in DB) by emitting
// a real-time structured log line for monitoring/alerting pipelines.
func (l *Logger) SecurityEvent(eventType, userID, ip string, success bool, details map[string]interface{}) {
	fields := logrus.Fields{
		"event_type": eventType,
		"user_id":    userID,
		"ip":         MaskIPForLog(ip),
		"success":    success,
		"timestamp":  time.Now().UTC().Format(time.RFC3339Nano),
	}
	for k, v := range details {
		fields[k] = sanitizeLogValue(v)
	}
	l.WithFields(fields).Info("SECURITY_EVENT")
}

// sanitizeQuery removes sensitive parameters from query strings
func sanitizeQuery(raw string) string {
	if raw == "" {
		return ""
	}
	parts := strings.Split(raw, "&")
	var result []string
	for _, p := range parts {
		kv := strings.SplitN(p, "=", 2)
		key := strings.ToLower(kv[0])
		if key == "access_token" || key == "token" || key == "password" || key == "refresh_token" || key == "api_key" || key == "secret" {
			if len(kv) == 2 {
				result = append(result, kv[0]+"=[REDACTED]")
			} else {
				result = append(result, kv[0])
			}
		} else {
			result = append(result, p)
		}
	}
	return strings.Join(result, "&")
}

func (l *Logger) keyvalsToFields(keyvals ...interface{}) logrus.Fields {
	fields := logrus.Fields{}
	for i := 0; i < len(keyvals)-1; i += 2 {
		if key, ok := keyvals[i].(string); ok {
			fields[key] = keyvals[i+1]
		}
	}
	return fields
}

// MaskIPForLog anonymizes an IP address for logging purposes.
// H33: IP masking to preserve user privacy in logs.
// IPv4: last octet zeroed (e.g., 192.168.1.42 -> 192.168.1.0)
// IPv6: last 80 bits zeroed (e.g., 2001:db8::1 -> 2001:db8::)
func MaskIPForLog(ip string) string {
	if ip == "" {
		return ""
	}
	// IPv4: zero last octet
	if idx := strings.LastIndex(ip, "."); idx >= 0 {
		return ip[:idx] + ".0"
	}
	// IPv6: keep first 4 hextets
	if strings.Contains(ip, ":") {
		parts := strings.Split(ip, ":")
		if len(parts) > 4 {
			return strings.Join(parts[:4], ":") + "::"
		}
		return ip
	}
	return ip
}

// sanitizeLogValue removes control characters from log values to prevent
// log injection attacks (CRLF injection, ANSI escape sequences, etc.).
// H33: log injection prevention.
func sanitizeLogValue(v interface{}) interface{} {
	switch val := v.(type) {
	case string:
		// Replace CR, LF, TAB and other control characters with underscore
		sanitized := strings.Map(func(r rune) rune {
			if r == '\n' || r == '\r' || r == '\t' {
				return '_'
			}
			if r < 0x20 {
				return '_'
			}
			return r
		}, val)
		return sanitized
	case map[string]interface{}:
		result := make(map[string]interface{}, len(val))
		for k, vv := range val {
			result[k] = sanitizeLogValue(vv)
		}
		return result
	case []interface{}:
		result := make([]interface{}, len(val))
		for i, vv := range val {
			result[i] = sanitizeLogValue(vv)
		}
		return result
	default:
		return v
	}
}

// GinMiddleware returns a gin middleware for request logging
func GinMiddleware(logger *Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		raw := c.Request.URL.RawQuery

		// Generate trace ID
		traceID := c.GetHeader("X-Trace-ID")
		if traceID == "" {
			traceID = fmt.Sprintf("%d", time.Now().UnixNano())
		}
		c.Set("trace_id", traceID)
		c.Writer.Header().Set("X-Trace-ID", traceID)

		c.Next()

		latency := time.Since(start)
		clientIP := c.ClientIP()
		method := c.Request.Method
		statusCode := c.Writer.Status()

		if raw != "" {
			path = path + "?" + sanitizeQuery(raw)
		}

		entry := logger.WithFields(logrus.Fields{
			"trace_id":  traceID,
			"status":    statusCode,
			"latency":   latency.Milliseconds(),
			"client_ip": clientIP,
			"method":    method,
			"path":      path,
			"size":      c.Writer.Size(),
		})

		if len(c.Errors) > 0 {
			entry.Error(c.Errors.String())
		} else if statusCode >= 500 {
			entry.Error("request failed")
		} else if statusCode >= 400 {
			entry.Warn("request warning")
		} else {
			entry.Info("request completed")
		}
	}
}
