package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/pflag"
)

// Client holds the fully-resolved client configuration.
type Client struct {
	Server     string // control URL, e.g. wss://connect.tunnel.example.com
	Token      string // auth token (resolved from inline or TokenFile)
	Name       string // requested subdomain (empty => random)
	Target     string // local address to forward to, e.g. 127.0.0.1:3000
	HostHeader string // Host presented to the local app (empty => public host)
	Insecure   bool   // skip TLS verification (self-signed dev servers)

	// Reconnect tuning.
	InitialBackoff time.Duration // first retry delay (then exponential)
	MaxBackoff     time.Duration // reconnect backoff ceiling
	MaxAttempts    int           // 0 = retry forever
	Jitter         bool          // randomize backoff to avoid thundering herd

	LogLevel  string
	LogFormat string
}

func clientDefaults() map[string]any {
	return map[string]any{
		"config":          "",
		"server":          "",
		"token":           "",
		"token_file":      "",
		"name":            "",
		"target":          "",
		"host_header":     "",
		"insecure":        false,
		"initial_backoff": "1s",
		"max_backoff":     "30s",
		"max_attempts":    0,
		"jitter":          true,
		"log_level":       "info",
		"log_format":      "auto",
	}
}

// RegisterClientFlags defines every client flag on f.
func RegisterClientFlags(f *pflag.FlagSet) {
	f.String("config", "", "path to config file (json|yaml|toml)")
	f.String("server", "", "control URL, e.g. wss://connect.tunnel.example.com")
	f.String("token", "", "auth token")
	f.String("token-file", "", "path to a file containing the auth token")
	f.String("name", "", "requested subdomain (empty => server assigns a random one)")
	f.String("host-header", "", "Host header presented to the local app (empty => public host)")
	f.Bool("insecure", false, "skip TLS verification of the server (dev only)")
	f.String("initial-backoff", "1s", "reconnect: first retry delay")
	f.String("max-backoff", "30s", "reconnect: backoff ceiling")
	f.Int("max-attempts", 0, "reconnect: max attempts (0 = forever)")
	f.Bool("jitter", true, "reconnect: randomize backoff to avoid thundering herd")
	f.String("log-level", "info", "log level: debug|info|warn|error")
	f.String("log-format", "auto", "log format: auto|text|json")
}

// LoadClient resolves the client configuration for `tunnel http <target>`.
func LoadClient(f *pflag.FlagSet, target string) (*Client, error) {
	c, err := loadClient(f, target)
	if err != nil {
		return nil, err
	}
	if c.Target == "" {
		return nil, fmt.Errorf("target is required (e.g. `tunnel http 3000`)")
	}
	return c, nil
}

// LoadClientBase resolves the client configuration without requiring a target —
// used by `tunnel auto`, which derives targets from discovery.
func LoadClientBase(f *pflag.FlagSet) (*Client, error) {
	return loadClient(f, "")
}

func loadClient(f *pflag.FlagSet, target string) (*Client, error) {
	k, err := load(clientDefaults(), f)
	if err != nil {
		return nil, err
	}

	token, err := readSecretFile(k.String("token"), firstNonEmpty(
		k.String("token_file"), os.Getenv(EnvPrefix+"TOKEN_FILE"),
	))
	if err != nil {
		return nil, err
	}

	if target == "" {
		target = k.String("target")
	}

	c := &Client{
		Server:         k.String("server"),
		Token:          token,
		Name:           k.String("name"),
		Target:         normalizeTarget(target),
		HostHeader:     k.String("host_header"),
		Insecure:       k.Bool("insecure"),
		InitialBackoff: mustDur(k.String("initial_backoff"), time.Second),
		MaxBackoff:     mustDur(k.String("max_backoff"), 30*time.Second),
		MaxAttempts:    k.Int("max_attempts"),
		Jitter:         k.Bool("jitter"),
		LogLevel:       k.String("log_level"),
		LogFormat:      k.String("log_format"),
	}

	if c.Server == "" {
		return nil, fmt.Errorf("server is required (set --server, TUNNEL_SERVER, or server: in the config file)")
	}
	return c, nil
}

// normalizeTarget accepts "3000", ":3000", "localhost:3000", or a full
// host:port and returns a dialable host:port (defaulting host to 127.0.0.1).
func normalizeTarget(t string) string {
	t = strings.TrimSpace(t)
	if t == "" {
		return ""
	}
	// Bare port number.
	if _, err := strconv.Atoi(t); err == nil {
		return "127.0.0.1:" + t
	}
	if strings.HasPrefix(t, ":") {
		return "127.0.0.1" + t
	}
	// Strip an accidental scheme.
	t = strings.TrimPrefix(t, "http://")
	t = strings.TrimPrefix(t, "https://")
	return t
}
