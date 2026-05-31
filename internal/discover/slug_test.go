package discover

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProjectSlugFromManifest(t *testing.T) {
	cases := []struct{ file, content, want string }{
		{"package.json", `{"name":"@acme/cool-app","version":"1.0.0"}`, "cool-app"},
		{"go.mod", "module github.com/meabed/superthing\n\ngo 1.26\n", "superthing"},
		{"Cargo.toml", "[package]\nname = \"rust-svc\"\n", "rust-svc"},
	}
	for _, c := range cases {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, c.file), []byte(c.content), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := projectSlug(dir); got != c.want {
			t.Errorf("%s: projectSlug = %q, want %q", c.file, got, c.want)
		}
	}
	// No manifest -> folder basename.
	d := filepath.Join(t.TempDir(), "My_Folder")
	_ = os.MkdirAll(d, 0o755)
	if got := projectSlug(d); got != "my-folder" {
		t.Errorf("fallback = %q, want my-folder", got)
	}
}
