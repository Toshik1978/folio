package notify

import (
	"bytes"
	"fmt"
	"path"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	// maxCrashEntries bounds the crash de-duplication map.
	maxCrashEntries = 256
	// crashTableFull is the delivery-status text recorded when a new crash
	// finds the de-duplication map full.
	crashTableFull = "crash de-duplication table full: crash sent without de-duplication"
)

// modulePath is the Go module path ("github.com/Toshik1978/folio"), derived
// from this package's own import path.
var modulePath = strings.TrimSuffix( //nolint:gochecknoglobals // derived once, never mutated
	reflect.TypeFor[crashEntry]().PkgPath(),
	"/internal/notify",
)

// recoverHelpers are the function-name prefixes (package-qualified, module
// relative) of the deferred recover closures that sit above the panicking
// frame; they are never the crash site.
var recoverHelpers = []string{ //nolint:gochecknoglobals // immutable lookup table
	"server.recoverer",
	"sync.(*Engine).safeSync.func",
	"sync.(*Engine).recoverCrash",
	"sync.(*watcher).handleSafely.func",
	"ingest.(*DeepIndexer).process.func",
	"ebook.(*Dispatcher).run.func",
	"db.(*WriteGuard).WithTx.func",
}

// crashSource is implemented by a panic value that was recovered and re-raised
// elsewhere (db.TxPanic) and so carries its own place and stack.
type crashSource interface {
	CrashPlace() string
	CrashStack() []byte
}

// crashEntry is one open de-duplication window.
type crashEntry struct {
	first      time.Time
	place, via string // via = "<outer place> (<outer detail>)" for a re-raised panic
	detail     string
	frame      string // firstAppFrame of the first occurrence
	text       string // the first occurrence's error text, capped at maxItemChars
	suppressed int
}

// where is the message's "Where:" value.
func (e *crashEntry) where() string {
	if e.via != "" {
		return "via " + e.via
	}

	return e.detail
}

// ReportCrash notifies a recovered panic at once on the urgent lane and
// counts identical crashes for CrashWindow instead of sending them. place is
// one of the documented crash places and detail says what was being done. It
// reports whether the crash message was queued: false when notifications are
// off or not ready, the crash was only counted, or the lane was full. It never
// blocks and never panics.
func (s *Service) ReportCrash(place, detail string, recovered any, stack []byte) (queued bool) {
	defer func() { _ = recover() }()

	cfg := s.cfg.Load()
	if cfg == nil || !cfg.Enabled || !cfg.Ready() {
		return false
	}

	now := s.now()
	e := newCrashEntry(now, place, detail, recovered, stack)
	key := e.place + "|" + e.frame + "|" + normalize(e.text)
	send, expired, full := s.admitCrash(key, e)
	if !send {
		return false
	}
	if expired != nil {
		s.enqueue(message{kind: KindCrashRepeat, text: crashRepeatText(s.prefix, expired)})
	}
	queued = s.enqueue(message{kind: KindCrash, text: crashText(s.prefix, e)})
	if full {
		s.status.recordFailure(now, crashTableFull)
	}

	return queued
}

// admitCrash records crash e under key. It reports send=false when e falls in
// an open window (only counted), the entry whose window had expired before the
// tick closed it (to report its repeats now), and full when the map had no
// room so e was not stored.
func (s *Service) admitCrash(key string, e *crashEntry) (send bool, expired *crashEntry, full bool) {
	s.crashMu.Lock()
	defer s.crashMu.Unlock()

	open, ok := s.crashes[key]
	if ok && e.first.Before(open.first.Add(s.timing.CrashWindow)) {
		open.suppressed++

		return false, nil, false
	}
	if ok && open.suppressed > 0 {
		expired = open
	}
	if !ok && len(s.crashes) >= maxCrashEntries {
		return true, nil, true
	}
	if s.crashes == nil {
		s.crashes = make(map[string]*crashEntry)
	}
	s.crashes[key] = e

	return true, expired, false
}

// tickCrashes closes every crash window that has expired at now, sending one
// follow-up for each that counted repeats.
func (s *Service) tickCrashes(now time.Time) {
	var repeats []message
	s.crashMu.Lock()
	for key, e := range s.crashes {
		if now.Before(e.first.Add(s.timing.CrashWindow)) {
			continue
		}
		if e.suppressed > 0 {
			repeats = append(repeats, message{kind: KindCrashRepeat, text: crashRepeatText(s.prefix, e)})
		}
		delete(s.crashes, key)
	}
	s.crashMu.Unlock()

	for _, m := range repeats {
		s.enqueue(m)
	}
}

// resetCrashes discards every open crash window.
func (s *Service) resetCrashes() {
	s.crashMu.Lock()
	defer s.crashMu.Unlock()
	clear(s.crashes)
}

// newCrashEntry describes one crash. A recovered crashSource supplies the
// place and stack, and the caller's place and detail become via.
func newCrashEntry(now time.Time, place, detail string, recovered any, stack []byte) *crashEntry {
	e := &crashEntry{first: now, place: place, detail: item(detail), text: item(fmt.Sprint(recovered))}
	if src, ok := recovered.(crashSource); ok {
		e.place = src.CrashPlace()
		stack = src.CrashStack()
		e.via = place
		if detail != "" {
			e.via += " (" + detail + ")"
		}
		e.via = item(e.via)
		e.detail = ""
	}
	e.frame = firstAppFrame(stack)

	return e
}

// crashText is the KindCrash message.
func crashText(prefix string, e *crashEntry) string {
	var b strings.Builder
	b.WriteString(prefix + "\nCrash recovered in " + e.place + "\nWhere: " + e.where())
	if e.frame != "" {
		b.WriteString("\nAt: " + e.frame)
	}
	b.WriteString("\nError: " + e.text + "\nIdentical crashes in the next hour will be counted, not sent.")

	return truncate(b.String())
}

// crashRepeatText is the KindCrashRepeat message sent when a window closes.
func crashRepeatText(prefix string, e *crashEntry) string {
	return truncate(prefix +
		"\nCrash in " + e.place + " repeated " + strconv.Itoa(e.suppressed) + " more times in the last hour" +
		"\nWhere: " + e.where() +
		"\nError: " + e.text)
}

// numberRun matches the parts of an error text that vary between otherwise
// identical crashes: hex addresses and decimal numbers.
var numberRun = regexp.MustCompile(`0x[0-9a-f]+|\d+`)

// normalize replaces every digit run and 0x address in s with "#".
func normalize(s string) string { return numberRun.ReplaceAllString(s, "#") }

// firstAppFrame returns the first frame of a debug.Stack trace that lies inside
// this module, rendered "pkg.Func (dir/file.go:line)" with a module-relative
// path. Runtime frames, the recover helpers and this package's own non-test
// code are skipped. It returns "" when there is no such frame.
func firstAppFrame(stack []byte) string {
	lines := bytes.Split(stack, []byte("\n"))
	for i := 0; i+1 < len(lines); i++ {
		fn := string(lines[i])
		loc := string(lines[i+1])
		if fn == "" || fn[0] == '\t' || !strings.HasPrefix(loc, "\t") {
			continue
		}
		i++
		if frame, ok := appFrame(fn, loc); ok {
			return frame
		}
	}

	return ""
}

// appFrame renders one stack frame (its function line and its "\tfile:line
// +0x.." line) when it belongs to the module and is not skipped.
func appFrame(fnLine, locLine string) (string, bool) {
	fn, ok := strings.CutPrefix(stripArgs(fnLine), modulePath+"/")
	if !ok {
		return "", false
	}
	slash := strings.LastIndex(fn, "/")
	dot := strings.Index(fn[slash+1:], ".")
	if dot < 0 {
		return "", false
	}
	dir, short := fn[:slash+1+dot], fn[slash+1:]

	file, _, _ := strings.Cut(strings.TrimPrefix(locLine, "\t"), " +0x")
	base := path.Base(file)
	if skipFrame(dir, short, base) {
		return "", false
	}

	return short + " (" + dir + "/" + base + ")", true
}

// skipFrame reports whether the frame of short (pkg.Func) in module dir and
// file base (file.go:line) is a recover helper or the notifier's own code.
func skipFrame(dir, short, base string) bool {
	if dir == "internal/notify" && !strings.Contains(base, "_test.go:") {
		return true
	}
	for _, h := range recoverHelpers {
		if strings.HasPrefix(short, h) {
			return true
		}
	}

	return false
}

// stripArgs drops the argument list from a stack function line, and the
// "created by " / " in goroutine N" decoration of a goroutine origin.
func stripArgs(line string) string {
	if rest, ok := strings.CutPrefix(line, "created by "); ok {
		line, _, _ = strings.Cut(rest, " in goroutine ")

		return line
	}
	if strings.HasSuffix(line, ")") {
		if i := strings.LastIndex(line, "("); i > 0 {
			return line[:i]
		}
	}

	return line
}
