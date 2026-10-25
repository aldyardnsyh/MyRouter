package guard

import (
	"fmt"

	"github.com/myrouter/myrouter/internal/logx"
)

// ZLogger adapts a logx.Logger to the Guard Logger interface.
type ZLogger struct{ L *logx.Logger }

// Warn implements Logger.
func (z ZLogger) Warn(msg string, fields ...Field) {
	z.L.Debug("guard warn "+msg, toFields(fields)...)
}

// Debug implements Logger.
func (z ZLogger) Debug(msg string, fields ...Field) {
	z.L.Debug("guard "+msg, toFields(fields)...)
}

// Info implements Logger.
func (z ZLogger) Info(msg string, fields ...Field) {
	z.L.Info("guard "+msg, toFields(fields)...)
}

func toFields(in []Field) []logx.Field {
	out := make([]logx.Field, 0, len(in))
	for _, f := range in {
		out = append(out, logx.F(f.Key, fmt.Sprint(f.Value)))
	}
	return out
}
