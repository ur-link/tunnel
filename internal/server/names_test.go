package server

import "testing"

func TestSanitizeLabel(t *testing.T) {
	cases := map[string]string{
		"MyApp":          "myapp",
		"my app":         "my-app",
		"  hello  ":      "hello",
		"a.b.c":          "a-b-c",
		"--x--":          "x",
		"foo__bar":       "foo-bar",
		"!!!":            "",
		"":               "",
		"UPPER_lower-99": "upper-lower-99",
	}
	for in, want := range cases {
		if got := sanitizeLabel(in); got != want {
			t.Errorf("sanitizeLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRandomNameShape(t *testing.T) {
	n := randomName(10)
	if len(n) != 10 {
		t.Fatalf("len = %d, want 10", len(n))
	}
	for _, r := range n {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
			t.Fatalf("unexpected char %q in %q", r, n)
		}
	}
}
