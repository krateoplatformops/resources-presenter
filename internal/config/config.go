package config

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/krateoplatformops/plumbing/env"
	"github.com/krateoplatformops/plumbing/pgutil"
	logutil "github.com/krateoplatformops/resources-presenter/internal/util/log"
)

const (
	serviceName           = "resources-presenter"
	defaultDbReadyTimeout = 4 * time.Minute
	defaultDebug          = false
)

type Config struct {
	Port           int
	Debug          bool
	DbURL          string
	DbReadyTimeout time.Duration
	Log            *slog.Logger

	// SigningKey is the HMAC key used to validate JWT tokens (HS256).
	SigningKey string
	// AuthnNS is the Kubernetes namespace where user clientconfig secrets are stored.
	AuthnNS string
}

func Setup() *Config {
	cfg := &Config{}

	cfgPort := flag.Int("port",
		env.ServicePort("PORT", 8080),
		"port to listen on",
	)

	cfgDebug := flag.Bool("debug",
		env.Bool("DEBUG", defaultDebug),
		"enable or disable debug logs",
	)

	cfgDbUser := flag.String("db-user",
		env.String("DB_USER", ""),
		"database connection username",
	)

	cfgDbPass := flag.String("db-pass",
		env.String("DB_PASS", ""),
		"database connection password",
	)

	cfgDbName := flag.String("db-name",
		env.String("DB_NAME", ""),
		"database name",
	)

	cfgDbHost := flag.String("db-host",
		env.String("DB_HOST", "localhost"),
		"database host",
	)
	cfgDbPort := flag.Int("db-port",
		env.Int("DB_PORT", 5432),
		"database port",
	)

	cfgDbParams := flag.String("db-params",
		env.String("DB_PARAMS", ""),
		"extra database query params (es: sslmode=disable&connect_timeout=5)",
	)

	cfgDbReadyTimeout := flag.Duration("db-ready-timeout",
		env.Duration("DB_READY_TIMEOUT", defaultDbReadyTimeout),
		"maximum time to wait for PostgreSQL to become ready",
	)

	cfgSignKey := flag.String("sign-key",
		env.String("SIGN_KEY", ""),
		"HMAC signing key for JWT validation",
	)

	cfgAuthnNS := flag.String("authn-ns",
		env.String("AUTHN_NS", ""),
		"Kubernetes namespace where user clientconfig secrets are stored",
	)

	flag.Usage = func() {
		fmt.Fprintln(flag.CommandLine.Output(), "Flags:")
		flag.PrintDefaults()
	}

	flag.Parse()

	cfg.Port = *cfgPort
	cfg.Debug = *cfgDebug
	cfg.DbReadyTimeout = *cfgDbReadyTimeout
	cfg.SigningKey = *cfgSignKey
	cfg.AuthnNS = *cfgAuthnNS

	cfg.Log = logutil.New(serviceName, cfg.Debug)

	params, err := parseDBParams(*cfgDbParams)
	if err != nil {
		cfg.Log.Error("invalid DB_PARAMS", "error", err)
		os.Exit(1)
	}

	cfg.DbURL, err = pgutil.ConnectionURL(
		*cfgDbUser,
		*cfgDbPass,
		*cfgDbHost,
		*cfgDbPort,
		*cfgDbName,
		params)
	if err != nil {
		cfg.Log.Error("unable to build DB_URL", slog.Any("err", err))
		os.Exit(1)
	}

	if cfg.Debug {
		cfg.Log.Debug("database connection URL", slog.String("cfg.DbURL", cfg.DbURL))
	}

	return cfg
}
