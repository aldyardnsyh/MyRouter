// Package logx provides a small structured JSON logger with secret redaction.
// Any field that looks like a credential, bearer header or master key is
// redacted before it reaches the sink (PRD §11 A1).
package logx

import (
	"encoding/json"
	"io"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Level is a log severity.
type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

var levelNames = map[Level]string{LevelDebug: "debug", LevelInfo: "info", LevelWarn: "warn", LevelError: "error"}

// Logger writes one JSON object per line to stdout.
type Logger struct {
	mu    *sync.Mutex
	level Level
	buf   map[string]any
}

// out is the log sink. It defaults to stdout and can be redirected to a file
// for the console-less desktop build.
var out io.Writer = os.Stdout

// SetOutput redirects log writes (used by the tray/desktop build, which has no
// console). Call it once at startup before serving traffic.
func SetOutput(w io.Writer) {
	if w != nil {
		out = w
	}
}

var (
	defaultOnce sync.Once
	defaultLog  *Logger

	// redactedKeys are field names whose values must never be logged.
	redactedKeys = map[string]bool{
		"password": true, "password_hash": true, "api_key": true, "apikey": true,
		"secret": true, "token": true, "authorization": true, "credential": true,
		"master_key": true, "ciphertext": true, "key": true, "bearer": true,
		"csrf": true, "csrf_token": true, "session": true, "cookie": true,
	}

	// secretPatterns catches credentials embedded in free-form strings.
	secretPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?i)bearer\s+[a-z0-9\-_\.=:]+`),
		regexp.MustCompile(`(sk-[a-zA-Z0-9_\-]{6,})`),
		regexp.MustCompile(`(mr-sk-[a-zA-Z0-9_\-]{4,})`),
		regexp.MustCompile(`(AIza[0-9A-Za-z_\-]{30,})`), // Google API keys
		regexp.MustCompile(`(gsk_[A-Za-z0-9]{30,})`),    // Groq
		regexp.MustCompile(`(xai-[A-Za-z0-9]{20,})`),    // xAI
		regexp.MustCompile(`(AKIA[0-9A-Z]{16})`),        // AWS access key id
	}
)

// New builds a logger at the given level.
func New(level Level) *Logger {
	return &Logger{mu: &sync.Mutex{}, level: level}
}

// Default returns a process-wide logger honouring MYROUTER_LOG_LEVEL.
func Default() *Logger {
	defaultOnce.Do(func() {
		lvl := LevelInfo
		switch strings.ToLower(os.Getenv("MYROUTER_LOG_LEVEL")) {
		case "debug":
			lvl = LevelDebug
		case "warn":
			lvl = LevelWarn
		case "error":
			lvl = LevelError
		}
		defaultLog = New(lvl)
	})
	return defaultLog
}

// Field is a key/value logging pair.
type Field struct {
	Key   string
	Value any
}

// F is a shorthand for constructing a Field.
func F(key string, value any) Field { return Field{Key: key, Value: value} }

func (l *Logger) log(level Level, msg string, fields []Field) {
	if l == nil || level < l.level {
		return
	}
	entry := make(map[string]any, len(fields)+2)
	entry["ts"] = time.Now().UTC().Format(time.RFC3339Nano)
	entry["level"] = levelNames[level]
	entry["msg"] = RedactText(msg)
	for _, f := range fields {
		entry[f.Key] = RedactValue(f.Key, f.Value)
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return
	}
	l.mu.Lock()
	_, _ = out.Write(append(line, '\n'))
	l.mu.Unlock()
}

// Debug logs at debug level.
func (l *Logger) Debug(msg string, fields ...Field) { l.log(LevelDebug, msg, fields) }

// Info logs at info level.
func (l *Logger) Info(msg string, fields ...Field) { l.log(LevelInfo, msg, fields) }

// Warn logs at warn level.
func (l *Logger) Warn(msg string, fields ...Field) { l.log(LevelWarn, msg, fields) }

// Error logs at error level.
func (l *Logger) Error(msg string, fields ...Field) { l.log(LevelError, msg, fields) }

// RedactValue masks values for sensitive keys. Matching is by substring so
// "user_api_key", "refreshToken" and "X-Api-Key" are all caught.
func RedactValue(key string, value any) any {
	k := strings.ToLower(key)
	// Deliberate exemption: the first-run setup token exists only to be copied
	// out of the log by the operator, and there is no other channel to deliver
	// it. It is single-use and voided as soon as an admin exists. Exact key
	// match only, so nothing else named like a token is affected.
	if k == "setup_token" {
		return value
	}
	if redactedKeys[k] || strings.Contains(k, "token") || strings.Contains(k, "secret") ||
		strings.Contains(k, "password") || strings.Contains(k, "apikey") || strings.Contains(k, "api_key") ||
		strings.Contains(k, "api-key") || strings.Contains(k, "api key") ||
		strings.Contains(k, "credential") || strings.Contains(k, "ciphertext") || strings.Contains(k, "csrf") ||
		k == "authorization" || k == "cookie" || k == "key" {
		if s, ok := value.(string); ok && s == "" {
			return ""
		}
		return "***"
	}
	if s, ok := value.(string); ok {
		return RedactText(s)
	}
	return value
}

// RedactText removes credential-looking substrings from free-form text.
func RedactText(s string) string {
	for _, re := range secretPatterns {
		s = re.ReplaceAllString(s, "***")
	}
	return s
}
