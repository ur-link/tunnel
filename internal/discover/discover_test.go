//go:build !windows

package discover

import (
	"slices"
	"testing"
)

// fakeRunner feeds canned lsof/ps output so discovery can be tested without
// touching the OS.
type fakeRunner struct {
	lsof, ps, cwd string
}

func (f fakeRunner) Run(name string, args ...string) (string, string, error) {
	switch {
	case name == "lsof" && slices.Contains(args, "cwd"):
		return f.cwd, "", nil
	case name == "lsof":
		return f.lsof, "", nil
	case name == "ps":
		return f.ps, "", nil
	}
	return "", "", nil
}

func newFake() fakeRunner {
	return fakeRunner{
		lsof: "p1000\ncnode\nn*:3000\np2000\ncpython3\nn127.0.0.1:5000\n",
		ps:   "1000 node\n2000 python3\n",
		// Dirs don't exist on disk, so projectRootDir falls back to the dir itself
		// and the slug is its basename.
		cwd: "p1000\nn/tmp/disco-test/code/webapp\np2000\nn/tmp/disco-test/other/api\n",
	}
}

func TestDiscoverSlugAndRuntime(t *testing.T) {
	svcs, err := New(newFake()).Discover(Config{})
	if err != nil {
		t.Fatal(err)
	}
	if len(svcs) != 2 {
		t.Fatalf("got %d services, want 2: %+v", len(svcs), svcs)
	}
	byslug := map[string]Service{}
	for _, s := range svcs {
		byslug[s.Slug] = s
	}
	if s, ok := byslug["webapp"]; !ok || s.Port != 3000 || s.Runtime != "node" {
		t.Errorf("webapp service wrong: %+v", s)
	}
	if s, ok := byslug["api"]; !ok || s.Port != 5000 || s.Runtime != "python" {
		t.Errorf("api service wrong: %+v", s)
	}
}

func TestDiscoverMainAndSuffixedSecondary(t *testing.T) {
	// One project (same root), three listeners:
	//   pid 1000 node :3000  (main dev server)
	//   pid 1000 node :3001  (HMR — same process, collapses to 3000)
	//   pid 2000 node :7007  (auxiliary tool — distinct process)
	// Expect: main "webapp" (3000) + secondary "webapp-7007".
	f := fakeRunner{
		lsof: "p1000\ncnode\nn*:3000\np1000\ncnode\nn*:3001\np2000\ncnode\nn127.0.0.1:7007\n",
		ps:   "1000 node\n2000 node\n",
		cwd:  "p1000\nn/srv/webapp\np2000\nn/srv/webapp\n",
	}
	svcs, err := New(f).Discover(Config{})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]int{}
	for _, s := range svcs {
		got[s.Slug] = s.Port
	}
	if got["webapp"] != 3000 {
		t.Errorf("main service should be webapp:3000, got %v", got)
	}
	if got["webapp-7007"] != 7007 {
		t.Errorf("secondary should be webapp-7007:7007, got %v", got)
	}
	if _, ok := got["webapp-3001"]; ok {
		t.Errorf("same-process HMR port 3001 should collapse, not appear: %v", got)
	}
	if len(svcs) != 2 {
		t.Fatalf("want 2 services (main + secondary), got %d: %v", len(svcs), got)
	}
}

func TestDiscoverPathContainment(t *testing.T) {
	// Only projects under /tm/disco-test/code should be exposed.
	svcs, err := New(newFake()).Discover(Config{Path: "/tmp/disco-test/code"})
	if err != nil {
		t.Fatal(err)
	}
	if len(svcs) != 1 || svcs[0].Slug != "webapp" {
		t.Fatalf("containment failed, got %+v", svcs)
	}
}
