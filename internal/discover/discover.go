// Package discover finds local listening dev servers and derives a stable slug
// for each from its project folder — ported from portless-tailscale-proxy. The
// tunnel client uses it to auto-expose services (`tunnel auto`), optionally
// contained to a single path so it only touches projects under that tree.
package discover

import (
	"bytes"
	"encoding/json"
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
		if slug := projectSlug(root); slug != "" {
			return slug
		}
	}
	if rt := classifyRuntime(l.Comm); rt != "" {
		return rt + "-" + strconv.Itoa(l.Port)
	}
	return "port-" + strconv.Itoa(l.Port)
}

// projectSlug derives the best slug for a project root: the declared package
// name (package.json "name" / go.mod module / Cargo.toml / pyproject) wins over
// the folder name, so a folder like "frontend" or "src" still maps to the real
// project identity. Falls back to the folder basename.
func projectSlug(root string) string {
	if name := jsonName(filepath.Join(root, "package.json")); name != "" {
		return slugify(stripScope(name))
	}
	if name := goModName(filepath.Join(root, "go.mod")); name != "" {
		return slugify(name)
	}
	if name := tomlName(filepath.Join(root, "Cargo.toml")); name != "" {
		return slugify(name)
	}
	if name := tomlName(filepath.Join(root, "pyproject.toml")); name != "" {
		return slugify(name)
	}
	return slugify(filepath.Base(root))
}

// stripScope turns "@acme/cool-app" into "cool-app".
func stripScope(name string) string {
	if i := strings.LastIndexByte(name, '/'); i >= 0 {
		return name[i+1:]
	}
	return name
}

func jsonName(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var pkg struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(b, &pkg)
	return strings.TrimSpace(pkg.Name)
}

func goModName(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		if line = strings.TrimSpace(line); strings.HasPrefix(line, "module ") {
			mod := strings.TrimSpace(strings.TrimPrefix(line, "module "))
			return filepath.Base(mod) // last path segment
		}
	}
	return ""
}

// tomlName extracts a top-level `name = "..."` (Cargo.toml / pyproject [project]).
func tomlName(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "name") {
			if i := strings.IndexByte(line, '='); i >= 0 {
				return strings.Trim(strings.TrimSpace(line[i+1:]), `"'`)
			}
		}
	}
	return ""
}

// buildServices turns raw listeners into routable services (mirrors
// portless-tailscale-proxy's discovery):
//   - filter to web runtimes (unless All) and apply the Path containment filter;
//   - group by project-root dir;
//   - within a project, collapse a single process's extra ports (dev server +
//     HMR) to that process's LOWEST port — one entry per process;
//   - the lowest-port process is the "main" service (clean slug); every other
//     distinct process is kept and suffixed with "-<port>" so auxiliary tools in
//     the same folder stay reachable;
//   - distinct projects sharing a folder name get a "-<port>" suffix to stay unique.
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

	// Per project: one entry per process (its lowest port), sorted by port.
	type project struct {
		procs []listener // distinct processes, lowest-port first; procs[0] is "main"
		slug  string
	}
	projects := make([]project, 0, len(order))
	slugCounts := map[string]int{}
	for _, key := range order {
		byPID := map[int]listener{}
		var pidOrder []int
		for _, l := range groups[key] {
			if cur, ok := byPID[l.PID]; !ok {
				byPID[l.PID] = l
				pidOrder = append(pidOrder, l.PID)
			} else if l.Port < cur.Port {
				byPID[l.PID] = l
			}
		}
		procs := make([]listener, 0, len(pidOrder))
		for _, pid := range pidOrder {
			procs = append(procs, byPID[pid])
		}
		sort.Slice(procs, func(i, j int) bool { return procs[i].Port < procs[j].Port })
		projects = append(projects, project{procs: procs, slug: baseSlug(procs[0])})
		slugCounts[baseSlug(procs[0])]++
	}

	var services []Service
	for _, p := range projects {
		mainSlug := p.slug
		if slugCounts[mainSlug] > 1 { // distinct projects share a folder name
			mainSlug += "-" + strconv.Itoa(p.procs[0].Port)
		}
		for i, proc := range p.procs {
			slug := mainSlug
			if i > 0 { // secondary process → suffix to stay reachable
				slug = mainSlug + "-" + strconv.Itoa(proc.Port)
			}
			services = append(services, Service{
				Slug: slug, Port: proc.Port, Runtime: classifyRuntime(proc.Comm),
				Dir: projectRootDir(proc.Cwd), PID: proc.PID,
			})
		}
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
