package client

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/ur-link/tunnel/internal/config"
	"github.com/ur-link/tunnel/internal/discover"
)

// AutoOptions configures discovery-based auto-exposure.
type AutoOptions struct {
	All      bool            // include non-web runtimes
	Runtimes map[string]bool // nil = all known web runtimes
	Path     string          // containment root; only projects under it
	Interval time.Duration   // rescan interval
}

// RunAuto discovers local dev servers under opts.Path and exposes each as its
// own tunnel (`<slug>-<namespace>.<domain>`), rescanning periodically to pick up
// servers that start/stop. Each service reuses the standard client (its own
// connection + reconnect), so one flaky service never affects the others.
func RunAuto(ctx context.Context, base *config.Client, opts AutoOptions, log *slog.Logger) error {
	d := discover.New(discover.ExecRunner{})
	if opts.Interval <= 0 {
		opts.Interval = 5 * time.Second
	}

	type managed struct {
		port   int
		cancel context.CancelFunc
	}
	running := map[string]*managed{} // slug -> managed
	defer func() {
		for _, m := range running {
			m.cancel()
		}
	}()

	reconcile := func() {
		svcs, err := d.Discover(discover.Config{All: opts.All, Runtimes: opts.Runtimes, Path: opts.Path})
		if err != nil {
			log.Warn("discovery failed", "err", err)
			return
		}
		seen := map[string]bool{}
		for _, svc := range svcs {
			seen[svc.Slug] = true
			if m, ok := running[svc.Slug]; ok {
				if m.port == svc.Port {
					continue // already exposed, unchanged
				}
				m.cancel() // same slug, new port -> restart
			}
			svcCtx, cancel := context.WithCancel(ctx)
			cfg := *base
			cfg.Name = svc.Slug
			cfg.Target = fmt.Sprintf("127.0.0.1:%d", svc.Port)
			running[svc.Slug] = &managed{port: svc.Port, cancel: cancel}
			log.Info("exposing service", "slug", svc.Slug, "port", svc.Port, "runtime", svc.Runtime)
			go func(c config.Client) {
				if err := New(&c, log).Run(svcCtx); err != nil && err != context.Canceled {
					log.Warn("service tunnel stopped", "slug", c.Name, "err", err)
				}
			}(cfg)
		}
		for slug, m := range running {
			if !seen[slug] {
				m.cancel()
				delete(running, slug)
				log.Info("unexposing service", "slug", slug)
			}
		}
	}

	reconcile()
	if len(running) == 0 {
		log.Warn("no dev servers discovered yet — will keep scanning", "path", opts.Path)
	}

	t := time.NewTicker(opts.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			reconcile()
		}
	}
}
