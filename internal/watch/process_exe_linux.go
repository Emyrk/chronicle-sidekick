package watch

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func (*systemProcs) Exe(_ context.Context, pid int32) (string, error) {
	return linuxProcessExe(filepath.Join("/proc", strconv.Itoa(int(pid))))
}

// linuxProcessExe resolves Wine's Windows argv[0] through the process's own
// prefix. /proc/PID/exe identifies the Wine loader, not the Windows executable.
// Keep returning the loader on failure: it cannot match a game installation.
func linuxProcessExe(procDir string) (string, error) {
	exe, err := os.Readlink(filepath.Join(procDir, "exe"))
	if err != nil {
		return "", err
	}
	switch filepath.Base(exe) {
	case "wine", "wine64", "wine-preloader", "wine64-preloader":
	default:
		return exe, nil
	}
	cmdline, err := os.ReadFile(filepath.Join(procDir, "cmdline"))
	if err != nil {
		return exe, nil
	}
	argv := strings.Split(string(cmdline), "\x00")
	program := argv[0]
	// Wine may retain the loader as argv[0] instead of replacing it with the
	// Windows executable. Only inspect argv[1] then, never arbitrary arguments.
	switch filepath.Base(program) {
	case "wine", "wine64", "wine-preloader", "wine64-preloader":
		if len(argv) < 2 {
			return exe, nil
		}
		program = argv[1]
	}
	var path string
	if filepath.IsAbs(program) {
		path = program
	} else if len(program) >= 3 && program[1] == ':' && (program[2] == '\\' || program[2] == '/') &&
		((program[0] >= 'A' && program[0] <= 'Z') || (program[0] >= 'a' && program[0] <= 'z')) {
		env, err := os.ReadFile(filepath.Join(procDir, "environ"))
		if err != nil {
			return exe, nil
		}
		vars := map[string]string{}
		for _, entry := range strings.Split(string(env), "\x00") {
			key, value, ok := strings.Cut(entry, "=")
			if ok {
				vars[key] = value
			}
		}
		prefix := vars["WINEPREFIX"]
		if prefix == "" && filepath.IsAbs(vars["HOME"]) {
			prefix = filepath.Join(vars["HOME"], ".wine")
		}
		if !filepath.IsAbs(prefix) {
			return exe, nil
		}
		drive := strings.ToLower(program[:1]) + ":"
		// Resolve the drive first, since mappings such as z: -> / must be applied
		// before cleaning the Windows path's components.
		root, err := filepath.EvalSymlinks(filepath.Join(prefix, "dosdevices", drive))
		if err != nil {
			return exe, nil
		}
		// Windows parent components cannot walk above the mapped drive root.
		relative := strings.TrimPrefix(filepath.Clean("/"+strings.ReplaceAll(program[3:], "\\", "/")), "/")
		path = filepath.Join(root, relative)
	} else {
		return exe, nil
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return exe, nil
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.Mode().IsRegular() {
		return exe, nil
	}
	return resolved, nil
}
