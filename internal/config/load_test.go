package config

import (
	"testing"

	flag "github.com/spf13/pflag"
)

func TestServerConfigPrecedence(t *testing.T) {
	// env supplies domain + http_addr; flag overrides http_addr; control_addr
	// falls back to the built-in default.
	t.Setenv("TUNNEL_DOMAIN", "env.example.com")
	t.Setenv("TUNNEL_HTTP_ADDR", ":8080")

	fs := flag.NewFlagSet("server", flag.ContinueOnError)
	RegisterServerFlags(fs)
	if err := fs.Parse([]string{"--http-addr", ":9999"}); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadServer(fs)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Domain != "env.example.com" {
		t.Errorf("domain = %q, want env value", cfg.Domain)
	}
	if cfg.HTTPAddr != ":9999" {
		t.Errorf("http_addr = %q, want flag override :9999", cfg.HTTPAddr)
	}
	if cfg.ControlAddr != ":7000" {
		t.Errorf("control_addr = %q, want default :7000", cfg.ControlAddr)
	}
	if cfg.YamuxKeepAlive.String() != "30s" {
		t.Errorf("yamux_keepalive = %v, want default 30s", cfg.YamuxKeepAlive)
	}
}

func TestServerConfigRequiresDomain(t *testing.T) {
	fs := flag.NewFlagSet("server", flag.ContinueOnError)
	RegisterServerFlags(fs)
	_ = fs.Parse(nil)
	if _, err := LoadServer(fs); err == nil {
		t.Fatal("expected error when domain is unset")
	}
}

func TestClientTargetNormalization(t *testing.T) {
	t.Setenv("TUNNEL_SERVER", "wss://connect.example.com")

	fs := flag.NewFlagSet("http", flag.ContinueOnError)
	RegisterClientFlags(fs)
	_ = fs.Parse(nil)

	cfg, err := LoadClient(fs, "3000")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Target != "127.0.0.1:3000" {
		t.Errorf("target = %q, want 127.0.0.1:3000", cfg.Target)
	}
	// Reconnect defaults.
	if cfg.InitialBackoff.String() != "1s" || cfg.MaxBackoff.String() != "30s" {
		t.Errorf("backoff defaults = %v/%v, want 1s/30s", cfg.InitialBackoff, cfg.MaxBackoff)
	}
	if cfg.MaxAttempts != 0 || !cfg.Jitter {
		t.Errorf("reconnect defaults = attempts %d jitter %v, want 0/true", cfg.MaxAttempts, cfg.Jitter)
	}
}
