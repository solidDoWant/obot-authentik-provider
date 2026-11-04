package launcher

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAddRegistry(t *testing.T) {
	for _, tc := range []struct {
		name, content, want string
	}{
		{
			name:    "appends",
			content: "export OBOT_SERVER_PROVIDER_REGISTRIES=\"/a,/b\"\n",
			want:    "/a,/b,/plugin",
		},
		{
			name:    "without a trailing newline",
			content: "export OBOT_SERVER_PROVIDER_REGISTRIES=\"/a\"",
			want:    "/a,/plugin",
		},
		{
			name: "with no registries",
			want: "/plugin",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			envrc := filepath.Join(t.TempDir(), ".envrc.providers")
			if err := os.WriteFile(envrc, []byte(tc.content), 0o644); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				if err := AddRegistry(envrc, "/plugin"); err != nil {
					t.Fatal(err)
				}
			}
			content, err := os.ReadFile(envrc)
			if err != nil {
				t.Fatal(err)
			}
			if n := strings.Count(string(content), "/plugin"); n != 1 {
				t.Fatalf("registry added %d times:\n%s", n, content)
			}
			if got := source(t, envrc); got != tc.want {
				t.Fatalf("registries %q, want %q", got, tc.want)
			}
		})
	}
}

// source reads the registries the way run.sh does, by sourcing the file in a shell.
func source(t *testing.T, envrc string) string {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), "/bin/sh", "-c", `. "$1"; printf %s "$OBOT_SERVER_PROVIDER_REGISTRIES"`, "sh", envrc)
	cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestAddRegistryFailures(t *testing.T) {
	dir := t.TempDir()
	if err := AddRegistry(filepath.Join(dir, "missing"), "/plugin"); err == nil || !strings.Contains(err.Error(), "doesn't exist") {
		t.Fatalf("missing file: error %v", err)
	}

	envrc := filepath.Join(dir, ".envrc.providers")
	if err := os.WriteFile(envrc, nil, 0o444); err != nil {
		t.Fatal(err)
	}
	if os.Getuid() != 0 {
		if err := AddRegistry(envrc, "/plugin"); err == nil || !strings.Contains(err.Error(), "must be writable") {
			t.Fatalf("read-only file: error %v", err)
		}
	}

	for _, registry := range []string{"relative", `/a"b`, "/a$b", "/a`b", `/a\b`, "/a\nb", "/a,b"} {
		if _, err := RegistryLine(registry); err == nil {
			t.Errorf("accepted registry path %q", registry)
		}
	}
}
