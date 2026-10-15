package core

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/ll-lite/vender/internal/config"
)

// BinaryName is the sing-box executable's file name (without a path) on the
// current platform. scripts/fetch-sing-box.sh drops it into
// dist/sing-box/<os>-<arch>/.
var BinaryName = binaryName(runtime.GOOS)

func binaryName(goos string) string {
	if goos == "windows" {
		return "sing-box.exe"
	}
	return "sing-box"
}

// ErrBinaryNotFound is returned by ResolveBinary when no sing-box executable
// could be located. Wrapping it lets callers test with errors.Is while the
// message still names every place that was searched.
var ErrBinaryNotFound = errors.New("core: sing-box executable not found")

// ResolveBinary locates the sing-box executable using vender's documented
// search order:
//
//  1. cfg.Engine.Binary — an explicit path from config.yaml;
//  2. a sing-box binary sitting next to the vender executable (the "bundled"
//     install: the files unpacked by scripts/fetch-sing-box.sh, copied beside
//     the vender binary);
//  3. sing-box on PATH.
//
// The returned path is absolute. When nothing is found the error lists all
// three locations, so the user knows exactly what to install and where.
func ResolveBinary(cfg config.Config) (string, error) {
	exeDir := ""
	if exe, err := os.Executable(); err == nil {
		exeDir = filepath.Dir(exe)
	}
	return resolveBinary(cfg.Engine.Binary, exeDir, os.Getenv("PATH"))
}

// resolveBinary is the pure, testable core of ResolveBinary: every ambient
// input (the bundled directory and the PATH) is a parameter, so tests need no
// real sing-box and no process environment.
func resolveBinary(configured, exeDir, pathEnv string) (string, error) {
	var tried []string

	if configured != "" {
		if abs, err := absolutize(configured); err == nil && isExecutableFile(abs) {
			return abs, nil
		}
		tried = append(tried, fmt.Sprintf("engine.binary from config.yaml: %s", configured))
	} else {
		tried = append(tried, "engine.binary from config.yaml: (not set)")
	}

	if exeDir != "" {
		bundled := filepath.Join(exeDir, BinaryName)
		if isExecutableFile(bundled) {
			return bundled, nil
		}
		tried = append(tried, fmt.Sprintf("next to the vender executable: %s", bundled))
	} else {
		tried = append(tried, "next to the vender executable: (vender executable path unknown)")
	}

	if found, err := lookPath(BinaryName, pathEnv); err == nil {
		if abs, absErr := absolutize(found); absErr == nil {
			return abs, nil
		}
		return found, nil
	}
	if pathEnv == "" {
		pathEnv = "(empty)"
	}
	tried = append(tried, fmt.Sprintf("%s on PATH: %s", BinaryName, pathEnv))

	return "", fmt.Errorf("%w; looked in:\n  - %s\ninstall it with scripts/fetch-sing-box.sh, or set engine.binary in config.yaml",
		ErrBinaryNotFound, strings.Join(tried, "\n  - "))
}

// lookPath is exec.LookPath against an explicit PATH value, so tests never
// depend on the machine's environment.
func lookPath(name, pathEnv string) (string, error) {
	if pathEnv == "" {
		return "", exec.ErrNotFound
	}
	for _, dir := range filepath.SplitList(pathEnv) {
		if dir == "" {
			continue
		}
		candidate := filepath.Join(dir, name)
		if isExecutableFile(candidate) {
			return candidate, nil
		}
	}
	return "", exec.ErrNotFound
}

func absolutize(path string) (string, error) {
	if filepath.IsAbs(path) {
		return filepath.Clean(path), nil
	}
	return filepath.Abs(path)
}

// isExecutableFile reports whether path is a regular file (symlinks followed)
// that the current user may execute.
func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	if runtime.GOOS == "windows" {
		return true
	}
	return info.Mode().Perm()&0o111 != 0
}
