package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/pflag"
)

// Server holds the fully-resolved server configuration.
type Server struct {
	Domain         string // base domain; tunnels become <name>.<domain>
	HTTPAddr       string // public HTTP edge listen address
	HTTPSAddr      string // public HTTPS edge (standalone TLS mode)
	ControlAddr    string // client control / WebSocket listener
	MetricsAddr    string // Prometheus /metrics + /_tunnel/status
	TLSMode        string // "acme" (standalone) | "off" (behind a proxy)
	TLSACMEEmail   string // Let's Encrypt account email
	TLSCacheDir    string // ACME cert cache directory
	TrustForwarded bool   // trust X-Forwarded-* (true behind Traefik/nginx)

	Tokens    string // inline token store (see auth.go for format)
	TokensRaw string // resolved token text (inline or from TokensFile)

	RandomNameLen     int           // length of fallback random slugs
	YamuxKeepAlive    time.Duration // dead-peer detection interval
	YamuxWindow       int           // per-stream flow-control window (bytes)
	ReadHeaderTimeout time.Duration // edge ReadHeaderTimeout
	IdleTimeout       time.Duration // edge IdleTimeout (no hard WriteTimeout)

	LogLevel  string
	LogFormat string
}

// serverDefaults are the built-in defaults (lowest precedence layer).
func serverDefaults() map[string]any {
	return map[string]any{
		"domain":              "",
		"http_addr":           ":80",
		"https_addr":          ":443",
		"control_addr":        ":7000",
		"metrics_addr":        ":9090",
		"tls_mode":            "acme",
		"tls_acme_email":      "",
		"tls_cache_dir":       "", // resolved to ~/.tunnel/certs below
		"trust_forwarded":     false,
		"tokens":              "",
		"tokens_file":         "",
		"random_name_len":     8,
		"yamux_keepalive":     "30s",
		"yamux_window":        1 << 20, // 1 MiB
		"read_header_timeout": "10s",
		"idle_timeout":        "120s",
		"log_level":           "info",
		"log_format":          "auto",
		"print_config":        false,
	}
}

// Redacted returns a copy safe to print: secret material is masked.
func (s *Server) Redacted() Server {
	c := *s
	if c.Tokens != "" {
		c.Tokens = "***redacted***"
	}
	if c.TokensRaw != "" {
		c.TokensRaw = "***redacted***"
	}
	return c
}

// RegisterServerFlags defines every server flag on f (kebab-case names that map
// to underscored config keys). Each flag mirrors a config key + env var.
func RegisterServerFlags(f *pflag.FlagSet) {
	f.String("config", "", "path to config file (json|yaml|toml); overrides search path")
	f.String("domain", "", "base domain; tunnels become <name>.<domain>")
	f.String("http-addr", ":80", "public HTTP edge listen address")
	f.String("https-addr", ":443", "public HTTPS edge listen address (tls-mode=acme)")
	f.String("control-addr", ":7000", "client control / WebSocket listener address")
	f.String("metrics-addr", ":9090", "Prometheus /metrics + /_tunnel/status address")
	f.String("tls-mode", "acme", "TLS mode: acme (standalone) | off (behind proxy)")
	f.String("acme-email", "", "Let's Encrypt account email (tls-mode=acme)")
	f.String("tls-cache-dir", "", "ACME certificate cache dir (default ~/.tunnel/certs)")
	f.Bool("trust-forwarded", false, "trust X-Forwarded-* headers (set behind Traefik/nginx)")
	f.String("tokens", "", "inline auth tokens, e.g. 'tok1:name1|name2,tok2'")
	f.String("tokens-file", "", "path to a file containing auth tokens")
	f.Int("random-name-len", 8, "length of fallback random subdomain slugs")
	f.String("yamux-keepalive", "30s", "yamux keepalive / dead-peer detection interval")
	f.Int("yamux-window", 1<<20, "yamux per-stream flow-control window in bytes")
	f.String("read-header-timeout", "10s", "edge server ReadHeaderTimeout")
	f.String("idle-timeout", "120s", "edge server IdleTimeout (no hard WriteTimeout)")
	f.String("log-level", "info", "log level: debug|info|warn|error")
	f.String("log-format", "auto", "log format: auto|text|json")
	f.Bool("print-config", false, "print the resolved configuration (secrets redacted) and exit")
}

// LoadServer resolves the server configuration across all layers.
func LoadServer(f *pflag.FlagSet) (*Server, error) {
	k, err := load(serverDefaults(), f)
	if err != nil {
		return nil, err
	}

	cacheDir := k.String("tls_cache_dir")
	if cacheDir == "" {
		if home, err := os.UserHomeDir(); err == nil {
			cacheDir = filepath.Join(home, ".tunnel", "certs")
		} else {
			cacheDir = ".tunnel-certs"
		}
	}

	tokensRaw, err := readSecretFile(k.String("tokens"), firstNonEmpty(
		k.String("tokens_file"), os.Getenv(EnvPrefix+"TOKENS_FILE"),
	))
	if err != nil {
		return nil, err
	}

	s := &Server{
		Domain:            k.String("domain"),
		HTTPAddr:          k.String("http_addr"),
		HTTPSAddr:         k.String("https_addr"),
		ControlAddr:       k.String("control_addr"),
		MetricsAddr:       k.String("metrics_addr"),
		TLSMode:           k.String("tls_mode"),
		TLSACMEEmail:      firstNonEmpty(k.String("acme_email"), k.String("tls_acme_email")),
		TLSCacheDir:       cacheDir,
		TrustForwarded:    k.Bool("trust_forwarded"),
		Tokens:            k.String("tokens"),
		TokensRaw:         tokensRaw,
		RandomNameLen:     k.Int("random_name_len"),
		YamuxWindow:       k.Int("yamux_window"),
		LogLevel:          k.String("log_level"),
		LogFormat:         k.String("log_format"),
		YamuxKeepAlive:    mustDur(k.String("yamux_keepalive"), 30*time.Second),
		ReadHeaderTimeout: mustDur(k.String("read_header_timeout"), 10*time.Second),
		IdleTimeout:       mustDur(k.String("idle_timeout"), 120*time.Second),
	}

	if err := s.validate(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Server) validate() error {
	if s.Domain == "" {
		return fmt.Errorf("domain is required (set --domain, TUNNEL_DOMAIN, or domain: in the config file)")
	}
	switch s.TLSMode {
	case "acme", "off":
	default:
		return fmt.Errorf("invalid tls-mode %q (want acme|off)", s.TLSMode)
	}
	if s.RandomNameLen < 4 {
		return fmt.Errorf("random-name-len must be >= 4")
	}
	return nil
}

// mustDur parses a Go duration string, falling back to def on error.
func mustDur(s string, def time.Duration) time.Duration {
	if s == "" {
		return def
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return def
	}
	return d
}
