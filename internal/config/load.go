// Package config implements the cloud-native, layered configuration shared by
// the tunnel server and client.
//
// Precedence, lowest to highest:
//
//	built-in defaults  →  config file (JSON|TOML|YAML)  →  env (TUNNEL_*)  →  CLI flags
//
// A zero-config run works: every key has a sensible default. Any single value
// can be overridden by any layer without touching the others.
//
// Values are read back through koanf's typed getters (k.String/k.Int/k.Bool),
// which coerce the string values coming from env vars and flags — so the same
// key works identically regardless of which layer set it.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/knadh/koanf/parsers/json"
	"github.com/knadh/koanf/parsers/toml"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/confmap"
	env "github.com/knadh/koanf/providers/env/v2"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
	"github.com/spf13/pflag"
)

const (
	// EnvPrefix prefixes every environment variable, e.g. TUNNEL_HTTP_ADDR.
	EnvPrefix = "TUNNEL_"
	// delim is the koanf key delimiter. Keys are flat (underscored), so the
	// delimiter is effectively unused, which keeps env/flag mapping unambiguous.
	delim = "."
)

// load builds a koanf instance by layering defaults, an optional config file,
// environment variables, and the explicitly-set flags from f.
//
// configKey is the flat key whose value points at the config file (e.g.
// "config"); it is consulted across all layers so --config / TUNNEL_CONFIG /
// a default search path all work.
func load(defaults map[string]any, f *pflag.FlagSet) (*koanf.Koanf, error) {
	k := koanf.New(delim)

	// 1. Defaults.
	if err := k.Load(confmap.Provider(defaults, delim), nil); err != nil {
		return nil, fmt.Errorf("load defaults: %w", err)
	}

	// Collect flag overrides early so a --config flag can select the file.
	flagOverrides := flagMap(f)

	// 2. Config file. Resolve its path from flag > env > default search path.
	path := firstNonEmpty(
		strFromMap(flagOverrides, "config"),
		os.Getenv(EnvPrefix+"CONFIG"),
	)
	if path == "" {
		path = searchConfigFile()
	}
	if path != "" {
		parser, err := parserFor(path)
		if err != nil {
			return nil, err
		}
		if err := k.Load(file.Provider(path), parser); err != nil {
			return nil, fmt.Errorf("load config file %q: %w", path, err)
		}
	}

	// 3. Environment: TUNNEL_HTTP_ADDR -> http_addr (strip prefix, lowercase).
	envProvider := env.Provider(delim, env.Opt{
		Prefix: EnvPrefix,
		TransformFunc: func(key, val string) (string, any) {
			return strings.ToLower(strings.TrimPrefix(key, EnvPrefix)), val
		},
	})
	if err := k.Load(envProvider, nil); err != nil {
		return nil, fmt.Errorf("load env: %w", err)
	}

	// 4. Flags (highest priority).
	if len(flagOverrides) > 0 {
		if err := k.Load(confmap.Provider(flagOverrides, delim), nil); err != nil {
			return nil, fmt.Errorf("load flags: %w", err)
		}
	}

	return k, nil
}

// flagMap returns only the flags that were explicitly set on the command line,
// keyed by their underscored name (--http-addr -> http_addr). Returning just
// the set flags is what makes flags override without clobbering other layers
// with their zero-value defaults.
func flagMap(f *pflag.FlagSet) map[string]any {
	out := map[string]any{}
	if f == nil {
		return out
	}
	f.Visit(func(fl *pflag.Flag) {
		key := strings.ReplaceAll(fl.Name, "-", "_")
		out[key] = fl.Value.String()
	})
	return out
}

// searchConfigFile looks for tunnel.{yaml,yml,toml,json} in the conventional
// locations and returns the first that exists, or "" if none do.
func searchConfigFile() string {
	var dirs []string
	if wd, err := os.Getwd(); err == nil {
		dirs = append(dirs, wd)
	}
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, ".tunnel"))
	}
	dirs = append(dirs, "/etc/tunnel")

	for _, dir := range dirs {
		for _, ext := range []string{"yaml", "yml", "toml", "json"} {
			p := filepath.Join(dir, "config."+ext)
			if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
				return p
			}
		}
	}
	return ""
}

// parserFor selects a koanf parser by file extension.
func parserFor(path string) (koanf.Parser, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		return json.Parser(), nil
	case ".yaml", ".yml":
		return yaml.Parser(), nil
	case ".toml":
		return toml.Parser(), nil
	default:
		return nil, fmt.Errorf("unsupported config extension %q (use .json/.yaml/.toml)", filepath.Ext(path))
	}
}

// readSecretFile resolves a "*_FILE" indirection: if fileKey is set, its file
// contents (trimmed) win over the inline value. This lets tokens/credentials
// be mounted as Docker/K8s secrets instead of inlined.
func readSecretFile(inline, filePath string) (string, error) {
	if strings.TrimSpace(filePath) == "" {
		return inline, nil
	}
	b, err := os.ReadFile(filePath)
	if err != nil {
		return "", fmt.Errorf("read secret file %q: %w", filePath, err)
	}
	return strings.TrimSpace(string(b)), nil
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func strFromMap(m map[string]any, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}
