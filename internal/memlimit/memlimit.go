// Package memlimit sizes the Go runtime's soft memory limit from the container
// it runs in.
//
// Without a limit the garbage collector only starts a cycle once the heap has
// doubled since the last one, so a burst of allocations can carry the process
// past a container's memory cap and get it OOM-killed even though most of that
// memory was already garbage. With the limit set just under the cap, the
// collector works harder as the process approaches it instead.
package memlimit

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"runtime/debug"
	"strconv"
	"strings"
)

const (
	// headroomPercent is the share of the container's limit given to the Go
	// runtime; the rest covers memory the runtime does not account for.
	headroomPercent = 90
	// unlimitedAbove treats absurdly large limits as no limit at all.
	unlimitedAbove = 1 << 62
)

// Apply sets the runtime's soft memory limit to headroomPercent of the cgroup
// v2 memory limit, unless the operator set GOMEMLIMIT or there is no limit.
// Failures are logged, never fatal: the process runs as it did before.
func Apply(log *slog.Logger) {
	limit, err := configure(os.DirFS("/"), os.Getenv, debug.SetMemoryLimit)
	switch {
	case err != nil:
		log.Warn("memory limit not set from cgroup", slog.Any("error", err))
	case limit > 0:
		log.Info("memory limit set from cgroup", slog.Int64("bytes", limit))
	}
}

// configure reads the cgroup limit from fsys (rooted at /) and passes the
// derived limit to set, returning it; 0 means nothing was set.
func configure(fsys fs.FS, getenv func(string) string, set func(int64) int64) (int64, error) {
	if getenv("GOMEMLIMIT") != "" {
		return 0, nil // already applied by the runtime at startup
	}
	cgroupMax, err := readCgroupMax(fsys)
	if err != nil || cgroupMax == 0 {
		return 0, err
	}
	limit := cgroupMax * headroomPercent / 100
	set(limit)

	return limit, nil
}

// readCgroupMax returns this process's cgroup v2 memory.max in bytes, or 0 when
// there is no cgroup v2 hierarchy or no limit.
func readCgroupMax(fsys fs.FS) (int64, error) {
	file := path.Join("sys/fs/cgroup", cgroupPath(fsys), "memory.max")
	raw, err := fs.ReadFile(fsys, file)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read %s: %w", file, err)
	}
	value := string(bytes.TrimSpace(raw))
	if value == "max" {
		return 0, nil
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("parse %s: unexpected value %q", file, value)
	}
	if n >= unlimitedAbove {
		return 0, nil
	}

	return n, nil
}

// cgroupPath returns the process's cgroup v2 path from /proc/self/cgroup ("/"
// inside its own cgroup namespace, the full path when it shares the host's),
// falling back to the hierarchy root.
func cgroupPath(fsys fs.FS) string {
	f, err := fsys.Open("proc/self/cgroup")
	if err != nil {
		return "."
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if p, ok := strings.CutPrefix(sc.Text(), "0::/"); ok {
			if p == "" {
				return "."
			}

			return p
		}
	}
	if sc.Err() != nil {
		return "." // unreadable: the root is the best remaining guess
	}

	return "."
}
