package log

import (
	"log/slog"
	"os"

	"github.com/krateoplatformops/plumbing/slogs/pretty"
)

func New(serviceName string, debugOn bool) *slog.Logger {
	logLevel := slog.LevelInfo
	if debugOn {
		logLevel = slog.LevelDebug
	}

	lh := pretty.New(&slog.HandlerOptions{
		Level:     logLevel,
		AddSource: false,
	},
		pretty.WithDestinationWriter(os.Stderr),
		pretty.WithColor(),
		pretty.WithOutputEmptyAttrs(),
	)

	log := slog.New(lh).With(slog.String("service", serviceName))
	if debugOn {
		log.Debug("environment variables", slog.Any("env", os.Environ()))
	}

	return log
}
