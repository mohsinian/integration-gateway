package logger

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
)

// Loggers holds three dedicated slog.Logger instances — one per concern.
//
//   - App:    application lifecycle (startup, shutdown, config, migrations)
//   - Server: HTTP traffic (requests, responses, handler events)
//   - Error:  errors only (failed migrations, external service failures)
//
// Each logger writes structured JSON to its own file AND human-readable text
// to stdout/stderr so that docker logs remains useful.
type Loggers struct {
	App    *slog.Logger
	Server *slog.Logger
	Error  *slog.Logger
	files  []*os.File
}

// Init creates the log directory (if needed) and opens three log files.
// Call Close() when the application shuts down.
func Init(logDir string) (*Loggers, error) {
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return nil, fmt.Errorf("create log dir %s: %w", logDir, err)
	}

	open := func(name string) (*os.File, error) {
		return os.OpenFile(
			filepath.Join(logDir, name),
			os.O_CREATE|os.O_WRONLY|os.O_APPEND,
			0644,
		)
	}

	appFile, err := open("app.log")
	if err != nil {
		return nil, fmt.Errorf("open app.log: %w", err)
	}

	serverFile, err := open("server.log")
	if err != nil {
		appFile.Close()
		return nil, fmt.Errorf("open server.log: %w", err)
	}

	errorFile, err := open("error.log")
	if err != nil {
		appFile.Close()
		serverFile.Close()
		return nil, fmt.Errorf("open error.log: %w", err)
	}

	newLogger := func(file, console *os.File, component string, level slog.Leveler) *slog.Logger {
		fileHandler := slog.NewJSONHandler(file, &slog.HandlerOptions{Level: level})
		consoleHandler := slog.NewTextHandler(console, &slog.HandlerOptions{Level: level})
		return slog.New(&teeHandler{fileHandler, consoleHandler}).With("component", component)
	}

	return &Loggers{
		App:    newLogger(appFile, os.Stdout, "app", slog.LevelInfo),
		Server: newLogger(serverFile, os.Stdout, "server", slog.LevelInfo),
		Error:  newLogger(errorFile, os.Stderr, "error", slog.LevelError),
		files:  []*os.File{appFile, serverFile, errorFile},
	}, nil
}

// Close flushes and closes all log files. Safe to call multiple times.
func (l *Loggers) Close() {
	for _, f := range l.files {
		f.Close()
	}
	l.files = nil
}

// ---------------------------------------------------------------------------
// teeHandler fans every log record out to two underlying handlers:
//   - fileHandler    → JSON (machine-parseable, persisted to disk)
//   - consoleHandler → Text  (human-readable, visible in docker logs)
// ---------------------------------------------------------------------------

type teeHandler struct {
	fileHandler    slog.Handler
	consoleHandler slog.Handler
}

func (t *teeHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return t.fileHandler.Enabled(ctx, level) || t.consoleHandler.Enabled(ctx, level)
}

func (t *teeHandler) Handle(ctx context.Context, r slog.Record) error {
	if t.fileHandler.Enabled(ctx, r.Level) {
		if err := t.fileHandler.Handle(ctx, r); err != nil {
			return err
		}
	}
	if t.consoleHandler.Enabled(ctx, r.Level) {
		if err := t.consoleHandler.Handle(ctx, r); err != nil {
			return err
		}
	}
	return nil
}

func (t *teeHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &teeHandler{
		fileHandler:    t.fileHandler.WithAttrs(attrs),
		consoleHandler: t.consoleHandler.WithAttrs(attrs),
	}
}

func (t *teeHandler) WithGroup(name string) slog.Handler {
	return &teeHandler{
		fileHandler:    t.fileHandler.WithGroup(name),
		consoleHandler: t.consoleHandler.WithGroup(name),
	}
}
