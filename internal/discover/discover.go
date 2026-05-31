// Package discover finds local listening dev servers and derives a stable slug
// for each from its project folder — ported from portless-tailscale-proxy. The
// tunnel client uses it to auto-expose services (`tunnel auto`), optionally
// contained to a single path so it only touches projects under that tree.
package discover

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Service is one discovered listening dev server.
type Service struct {
	Slug    string // derived URL/subdomain label
	Port    int    // listening port (loopback)
	Runtime string // node|bun|python|… or "" (unknown)
	Dir     string // project root directory (may be "")
	PID     int
}

// Config bundles discovery filters.
type Config struct {
	All      bool            // include non-web runtimes too
	Runtimes map[string]bool // nil = all known web runtimes
	Path     string          // containment root; only projects under it ("" = no filter)
}

// Runner runs external commands (abstracted for tests).
type Runner interface {
	Run(name string, args ...string) (stdout, stderr string, err error)
}

// ExecRunner runs real commands.
type ExecRunner struct{}

func (ExecRunner) Run(name string, args ...string) (string, string, error) {
	cmd := exec.Command(name, args...)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	err := cmd.Run()
	return out.String(), errb.String(), err
}

// listener is a raw OS-level listening socket (pre-classification).
type listener struct {
	Port int
	PID  int
	Comm string
	Cwd  string
}

// Discoverer lists services using an injected Runner.
type Discoverer struct{ run Runner }

func New(r Runner) *Discoverer { return &Discoverer{run: r} }

// Discover returns the filtered, de-duplicated services.
func (d *Discoverer) Discover(cfg Config) ([]Service, error) {
	ls, err := d.listeners()
	if err != nil {
		return nil, err
	}
	return buildServices(ls, cfg), nil
}

var knownRuntimes = map[string]string{
	"node": "node", "bun": "bun", "deno": "deno",
	"python": "python", "python2": "python", "python3": "python",
	"uvicorn": "python", "gunicorn": "python", "hypercorn": "python",
	"flask": "python", "waitress-serve": "python",
	"ruby": "ruby", "puma": "ruby", "unicorn": "ruby", "rackup": "ruby",
	"rails": "ruby", "thin": "ruby",
	"php": "php", "php-fpm": "php",
	"go":   "go",
	"java": "java", "dotnet": "dotnet",
	"beam": "elixir", "beam.smp": "elixir", "perl": "perl",
}

var projectMarkers = []string{
	".git", "package.json", "deno.json", "bun.lockb", "go.mod",
	"pyproject.toml", "requirements.txt", "Pipfile", "setup.py",
	"Gemfile", "composer.json", "Cargo.toml",
	"pom.xml", "build.gradle", "build.gradle.kts", "mix.exs",
}

func classifyRuntime(comm string) string {
	base := strings.TrimSuffix(strings.ToLower(filepath.Base(comm)), ".exe")
	if rt, ok := knownRuntimes[base]; ok {
		return rt
	}
	switch {
	case strings.HasPrefix(base, "python"):
		return "python"
	case strings.Contains(comm, "go-build"):
		return "go"
	}
	return ""
}

// projectRootDir walks up from dir to the nearest directory holding a project
// marker; falls back to dir, or "".
func projectRootDir(dir string) string {
	if dir == "" || dir == string(filepath.Separator) {
		return ""
	}
	d := dir
	for {
		for _, m := range projectMarkers {
			if _, err := os.Stat(filepath.Join(d, m)); err == nil {
				return d
			}
		}
		parent := filepath.Dir(d)
		if parent == d {
			break
		}
		d = parent
	}
	return dir
}

var slugUnsafe = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(name string) string {
	return strings.Trim(slugUnsafe.ReplaceAllString(strings.ToLower(name), "-"), "-")
}

func baseSlug(l listener) string {
	if root := projectRootDir(l.Cwd); root != "" {
		if slug := slugify(filepath.Base(root)); slug != "" {
			return slug
		}
	}
	if rt := classifyRuntime(l.Comm); rt != "" {
		return rt + "-" + strconv.Itoa(l.Port)
	}
	return "port-" + strconv.Itoa(l.Port)
}

func moreRecent(a, b listener) bool {
	if a.PID != b.PID {
		return a.PID > b.PID
	}
	return a.Port > b.Port
}

// buildServices filters listeners to web runtimes (unless All), applies the Path
// containment filter, and collapses listeners of the same project into one
// service (most-recent instance), suffixing with -<port> on slug collisions.
func buildServices(listeners []listener, cfg Config) []Service {
	absPath := ""
	if cfg.Path != "" {
		if p, err := filepath.Abs(cfg.Path); err == nil {
			absPath = resolveSymlinks(p)
		}
	}

	groups := map[string][]listener{}
	var order []string
	for _, l := range listeners {
		rt := classifyRuntime(l.Comm)
		if !cfg.All && (rt == "" || (cfg.Runtimes != nil && !cfg.Runtimes[rt])) {
			continue
		}
		root := projectRootDir(l.Cwd)
		if absPath != "" && !underPath(resolveSymlinks(root), absPath) {
			continue // containment: only projects under --path
		}
		key := root
		if key == "" {
			key = "\x00port:" + strconv.Itoa(l.Port)
		}
		if _, seen := groups[key]; !seen {
			order = append(order, key)
		}
		groups[key] = append(groups[key], l)
	}

	type chosen struct {
		best listener
		slug string
	}
	picks := make([]chosen, 0, len(order))
	slugCounts := map[string]int{}
	for _, key := range order {
		ls := groups[key]
		best := ls[0]
		for _, l := range ls[1:] {
			if moreRecent(l, best) {
				best = l
			}
		}
		slug := baseSlug(best)
		picks = append(picks, chosen{best: best, slug: slug})
		slugCounts[slug]++
	}

	var services []Service
	for _, p := range picks {
		slug := p.slug
		if slugCounts[slug] > 1 {
			slug += "-" + strconv.Itoa(p.best.Port)
		}
		services = append(services, Service{
			Slug: slug, Port: p.best.Port, Runtime: classifyRuntime(p.best.Comm),
			Dir: projectRootDir(p.best.Cwd), PID: p.best.PID,
		})
	}
	sort.Slice(services, func(i, j int) bool { return services[i].Slug < services[j].Slug })
	return services
}

// resolveSymlinks returns the symlink-resolved path, or the input unchanged if
// it can't be resolved (e.g. doesn't exist). Handles cases like macOS /tmp ->
// /private/tmp and symlinked home directories so containment matches reliably.
func resolveSymlinks(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// underPath reports whether dir is within root (or equal).
func underPath(dir, root string) bool {
	if dir == "" {
		return false
	}
	rel, err := filepath.Rel(root, dir)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
