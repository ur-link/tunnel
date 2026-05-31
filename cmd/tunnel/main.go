// Command tunnel is a self-hosted ngrok/Funnel alternative: one binary that
// runs either the public edge server or a client that forwards a local service.
//
//	tunnel server --domain tunnel.example.com
//	tunnel http 3000 --server wss://connect.tunnel.example.com --token <tok>
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	flag "github.com/spf13/pflag"

	"github.com/ur-link/tunnel/internal/client"
	"github.com/ur-link/tunnel/internal/config"
	"github.com/ur-link/tunnel/internal/logging"
	"github.com/ur-link/tunnel/internal/server"
)

// version is overridable at build time: -ldflags "-X main.version=v1.2.3".
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	cmd, args := os.Args[1], os.Args[2:]
	var err error
	switch cmd {
	case "server":
		err = runServer(args)
	case "http":
		err = runClient(args)
	case "auto":
		err = runAuto(args)
	case "version", "--version", "-v":
		fmt.Printf("tunnel %s\n", version)
	case "help", "--help", "-h":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", cmd)
		usage()
		os.Exit(2)
	}

	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func runServer(args []string) error {
	fs := flag.NewFlagSet("server", flag.ContinueOnError)
	config.RegisterServerFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.LoadServer(fs)
	if err != nil {
		return err
	}
	if pc, _ := fs.GetBool("print-config"); pc {
		return printJSON(cfg.Redacted())
	}

	log := logging.New(cfg.LogLevel, cfg.LogFormat)
	ctx, stop := signalContext()
	defer stop()
	return server.New(cfg, log).Run(ctx)
}

func runClient(args []string) error {
	fs := flag.NewFlagSet("http", flag.ContinueOnError)
	config.RegisterClientFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := config.LoadClient(fs, fs.Arg(0))
	if err != nil {
		return err
	}

	log := logging.New(cfg.LogLevel, cfg.LogFormat)
	client.Version = version
	ctx, stop := signalContext()
	defer stop()
	if err := client.New(cfg, log).Run(ctx); err != nil && err != context.Canceled {
		return err
	}
	return nil
}

func runAuto(args []string) error {
	fs := flag.NewFlagSet("auto", flag.ContinueOnError)
	config.RegisterClientFlags(fs)
	fs.String("path", "", "containment path: only expose projects under it (default: cwd)")
	fs.Bool("all", false, "include non-web runtimes (default: known web runtimes only)")
	fs.String("runtimes", "", "comma-separated runtimes to include, e.g. node,bun (default: all known)")
	fs.String("interval", "5s", "how often to rescan for new/removed dev servers")
	if err := fs.Parse(args); err != nil {
		return err
	}

	base, err := config.LoadClientBase(fs)
	if err != nil {
		return err
	}

	path := fs.Arg(0)
	if path == "" {
		path, _ = fs.GetString("path")
	}
	if path == "" {
		path, _ = os.Getwd()
	}
	all, _ := fs.GetBool("all")
	rtStr, _ := fs.GetString("runtimes")
	intervalStr, _ := fs.GetString("interval")
	interval, _ := time.ParseDuration(intervalStr)

	opts := client.AutoOptions{All: all, Runtimes: parseRuntimes(rtStr), Path: path, Interval: interval}
	log := logging.New(base.LogLevel, base.LogFormat)
	client.Version = version
	ctx, stop := signalContext()
	defer stop()
	if err := client.RunAuto(ctx, base, opts, log); err != nil && err != context.Canceled {
		return err
	}
	return nil
}

// parseRuntimes turns "node,bun" into a set; "" yields nil (all known).
func parseRuntimes(s string) map[string]bool {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	m := map[string]bool{}
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			m[p] = true
		}
	}
	return m
}

// signalContext returns a context cancelled on SIGINT/SIGTERM.
func signalContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}

func printJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func usage() {
	fmt.Fprint(os.Stderr, `tunnel — self-hosted ngrok alternative

Usage:
  tunnel server [flags]          Run the public edge server
  tunnel http <target> [flags]   Forward a single local service through a tunnel
  tunnel auto [path] [flags]     Discover & expose all dev servers under a path
  tunnel version                 Print version
  tunnel help                    Show this help

Examples:
  tunnel server --domain tunnel.example.com --acme-email you@example.com
  tunnel http 3000 --server wss://connect.tunnel.example.com --token <tok> --name myapp
  tunnel auto ~/code --server wss://connect.tunnel.example.com --token <tok>

All flags are also settable via TUNNEL_* env vars and a config file
(--config tunnel.yaml|json|toml). Run a subcommand with --help for its flags.
`)
}
