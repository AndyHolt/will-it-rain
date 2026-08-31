package main

import (
	"log/slog"
	"os"
)

// newLogger writes structured JSON to stdout.
//
// Cloud Run reads a JSON log line's "severity" and "message" fields, and slog
// spells them "level" and "msg", so the two keys are renamed on the way out —
// otherwise every line lands as the default severity with the message buried
// in the payload. Stdout rather than stderr for the same reason the Python
// backend chose it: Cloud Run maps stderr to ERROR.
func newLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, cloudLoggingOptions()))
}

// cloudLoggingOptions is the key renaming, split out so a test can record
// through the same handler the service logs through — asserting on a severity
// the deployed service does not emit would pin nothing.
func cloudLoggingOptions() *slog.HandlerOptions {
	return &slog.HandlerOptions{
		Level: slog.LevelInfo,
		ReplaceAttr: func(_ []string, attr slog.Attr) slog.Attr {
			switch attr.Key {
			case slog.LevelKey:
				attr.Key = "severity"
				// Cloud Logging's severities are slog's level names, except
				// that it spells WARN as WARNING.
				if level, ok := attr.Value.Any().(slog.Level); ok && level == slog.LevelWarn {
					attr.Value = slog.StringValue("WARNING")
				}
			case slog.MessageKey:
				attr.Key = "message"
			}
			return attr
		},
	}
}
