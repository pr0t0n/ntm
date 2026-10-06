package config

import "os"

// Config holds runtime settings sourced from environment variables so the
// container can be configured entirely through docker-compose.
type Config struct {
	ListenAddr   string
	DatabaseURL  string
	InstallToken string // token operators paste into the agent at install time
	TLSCert      string
	TLSKey       string
}

func Load() Config {
	return Config{
		ListenAddr:   env("NTM_LISTEN", ":8443"),
		DatabaseURL:  env("NTM_DATABASE_URL", "postgres://ntm:ntm@db:5432/ntm?sslmode=disable"),
		InstallToken: env("NTM_INSTALL_TOKEN", "change-me-install-token"),
		TLSCert:      env("NTM_TLS_CERT", "/certs/server.crt"),
		TLSKey:       env("NTM_TLS_KEY", "/certs/server.key"),
	}
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
