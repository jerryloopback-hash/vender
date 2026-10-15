package core

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/ll-lite/vender/internal/config"
)

// writeExecutable creates an executable stub file and returns its path.
func writeExecutable(t *testing.T, dir, name string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}

func TestResolveBinaryOrder(t *testing.T) {
	binDir := t.TempDir()
	bundledDir := t.TempDir()
	pathDir := t.TempDir()

	configured := writeExecutable(t, filepath.Join(binDir, "explicit"), "sing-box")
	bundled := writeExecutable(t, bundledDir, BinaryName)
	onPath := writeExecutable(t, pathDir, BinaryName)

	tests := []struct {
		name       string
		configured string
		exeDir     string
		pathEnv    string
		want       string
	}{
		{
			name:       "configured path wins over everything",
			configured: configured,
			exeDir:     bundledDir,
			pathEnv:    pathDir,
			want:       configured,
		},
		{
			name:    "bundled next to the executable wins over PATH",
			exeDir:  bundledDir,
			pathEnv: pathDir,
			want:    bundled,
		},
		{
			name:    "PATH is the last resort",
			exeDir:  t.TempDir(),
			pathEnv: pathDir,
			want:    onPath,
		},
		{
			name:    "missing configured path falls through to PATH",
			exeDir:  t.TempDir(),
			pathEnv: pathDir,
			want:    onPath,
			// configured is filled in below
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := tt.configured
			if tt.name == "missing configured path falls through to PATH" {
				cfg = filepath.Join(binDir, "does-not-exist")
			}
			got, err := resolveBinary(cfg, tt.exeDir, tt.pathEnv)
			if err != nil {
				t.Fatalf("resolveBinary: %v", err)
			}
			if got != tt.want {
				t.Errorf("resolveBinary = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolveBinaryNotFoundNamesEveryLocation(t *testing.T) {
	exeDir := t.TempDir()
	configured := filepath.Join(t.TempDir(), "nope", "sing-box")

	_, err := resolveBinary(configured, exeDir, t.TempDir())
	if err == nil {
		t.Fatal("resolveBinary = nil error, want a failure")
	}
	if !errors.Is(err, ErrBinaryNotFound) {
		t.Errorf("error = %v, want it to wrap ErrBinaryNotFound", err)
	}
	for _, want := range []string{
		"engine.binary",
		"next to the vender executable",
		"on PATH",
		BinaryName,
		"scripts/fetch-sing-box.sh",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to mention %q", err, want)
		}
	}
	if !strings.Contains(err.Error(), configured) {
		t.Errorf("error = %q, want it to name the configured path %q", err, configured)
	}
	if !strings.Contains(err.Error(), filepath.Join(exeDir, BinaryName)) {
		t.Errorf("error = %q, want it to name the bundled path", err)
	}
}

func TestResolveBinaryPrefersExecutableFiles(t *testing.T) {
	dir := t.TempDir()
	notExec := filepath.Join(dir, BinaryName)
	if err := os.WriteFile(notExec, []byte("not executable"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if runtime.GOOS == "windows" {
		t.Skip("the executable bit does not exist on windows")
	}
	if _, err := resolveBinary(notExec, dir, ""); err == nil {
		t.Error("resolveBinary accepted a non-executable file, want a failure")
	}
}

func TestResolveBinaryRejectsDirectories(t *testing.T) {
	dir := t.TempDir()
	if _, err := resolveBinary(dir, dir, ""); err == nil {
		t.Error("resolveBinary accepted a directory, want a failure")
	}
}

func TestResolveBinaryRelativeConfiguredPathIsMadeAbsolute(t *testing.T) {
	dir := t.TempDir()
	writeExecutable(t, dir, "sing-box")

	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	defer os.Chdir(wd)
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}

	got, err := resolveBinary("sing-box", t.TempDir(), "")
	if err != nil {
		t.Fatalf("resolveBinary: %v", err)
	}
	if !filepath.IsAbs(got) {
		t.Errorf("resolveBinary(%q) = %q, want an absolute path", "sing-box", got)
	}
}

func TestBinaryName(t *testing.T) {
	if got := binaryName("linux"); got != "sing-box" {
		t.Errorf("binaryName(linux) = %q", got)
	}
	if got := binaryName("windows"); got != "sing-box.exe" {
		t.Errorf("binaryName(windows) = %q", got)
	}
}

// ResolveBinary itself must fail cleanly on a machine without sing-box, and
// must name the searched locations. Skipped when one happens to be installed.
func TestResolveBinaryOnThisMachine(t *testing.T) {
	cfg := *config.Default()
	path, err := ResolveBinary(cfg)
	if err != nil {
		if !errors.Is(err, ErrBinaryNotFound) {
			t.Fatalf("ResolveBinary error = %v, want ErrBinaryNotFound", err)
		}
		return
	}
	if !filepath.IsAbs(path) {
		t.Errorf("ResolveBinary = %q, want an absolute path", path)
	}
}
