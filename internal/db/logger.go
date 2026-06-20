package db

import (
	"fmt"
	"log/slog"
)

type slogger struct {
	*slog.Logger

	exitFunc func(int)
}

func newSlogger(log *slog.Logger, exitFunc func(int)) *slogger {
	return &slogger{
		Logger:   log,
		exitFunc: exitFunc,
	}
}

// Printf implements the Printf method for goose.Logger.
func (sl *slogger) Printf(format string, v ...any) {
	sl.Info(fmt.Sprintf(format, v...))
}

// Fatalf implements goose.Logger. Goose's contract is that Fatalf stops
// execution (matching log.Fatalf), so it must terminate after logging — a
// log-only implementation would let a failed migration continue into serving.
func (sl *slogger) Fatalf(format string, v ...any) {
	sl.Error(fmt.Sprintf(format, v...))
	sl.exitFunc(1)
}
