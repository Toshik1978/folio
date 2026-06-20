package sync

import (
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	stdsync "sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/Toshik1978/folio/internal/db/dbq"
	"github.com/Toshik1978/folio/internal/libtype"
)

// initWatcher creates the file-system watcher and starts its event loop. The
// watcher is created unconditionally (even with no folder libraries yet) so that
// folder libraries added at runtime can be picked up via reconcileWatches without
// a restart. The actual directory watches are attached by reconcileWatches, which
// Reschedule calls on startup and on every library change.
func (e *Engine) initWatcher() error {
	w, err := newWatcher(e.log, e.debounce, e.TriggerLibrary)
	if err != nil {
		return err
	}
	w.run()
	e.watcher = w

	return nil
}

// reconcileWatches brings the fsnotify watch set in line with the current folder
// libraries: it starts watching newly added (or re-pathed) folder libraries and
// stops watching those removed, awaiting deletion, or whose path changed. Called
// from Reschedule, so library create/update/delete picks up watches at runtime
// rather than only at startup.
func (e *Engine) reconcileWatches(srcs []dbq.Library) {
	if e.watcher == nil {
		return
	}
	want := make(map[int64]string)
	for i := range srcs {
		if srcs[i].Type == libtype.Folder && srcs[i].Status != statusPendingPurge {
			want[srcs[i].ID] = srcs[i].Path
		}
	}
	e.watcher.reconcile(want)
}

// watcher debounces fsnotify events per library and fires trigger after a quiet
// period, avoiding re-indexing partially written files.
type watcher struct {
	log      *slog.Logger
	fsw      *fsnotify.Watcher
	debounce time.Duration
	trigger  func(libraryID int64)
	stop     chan struct{}

	reconcileMu stdsync.Mutex // serializes reconcile() against itself

	mu     stdsync.Mutex
	dirs   map[string]int64 // watched directory -> library ID
	roots  map[int64]string // library ID -> watched root path (for reconcile)
	timers map[int64]*time.Timer
}

func newWatcher(log *slog.Logger, debounce time.Duration, trigger func(int64)) (*watcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("create fsnotify watcher: %w", err)
	}

	return &watcher{
		fsw:      fsw,
		log:      log,
		debounce: debounce,
		trigger:  trigger,
		stop:     make(chan struct{}),
		dirs:     make(map[string]int64),
		roots:    make(map[int64]string),
		timers:   make(map[int64]*time.Timer),
	}, nil
}

// Close stops the event loop, releases the watcher, and cancels pending timers.
func (w *watcher) Close() {
	close(w.stop)
	_ = w.fsw.Close()
	w.mu.Lock()
	for _, t := range w.timers {
		t.Stop()
	}
	w.mu.Unlock()
}

// reconcile brings the watch set in line with want (library ID -> root path): it
// unwatches libraries that vanished or changed root and watches new or re-pathed
// ones. reconcileMu serializes concurrent reconciles (Reschedule may be called
// from several API handlers); the per-directory map is guarded by w.mu, which is
// only held briefly so the event loop never stalls behind a directory walk.
func (w *watcher) reconcile(want map[int64]string) {
	w.reconcileMu.Lock()
	defer w.reconcileMu.Unlock()

	w.mu.Lock()
	current := make(map[int64]string, len(w.roots))
	maps.Copy(current, w.roots)
	w.mu.Unlock()

	for id, root := range current {
		if want[id] != root {
			w.unwatch(id)
		}
	}
	for id, root := range want {
		if current[id] != root {
			if err := w.watch(id, root); err != nil {
				w.log.Error("watch library",
					slog.Int64("library", id), slog.String("path", root), slog.Any("error", err))
			}
		}
	}
}

// watch registers every directory under root for libraryID (fsnotify is not
// recursive) and records the root so reconcile can detect a later path change.
func (w *watcher) watch(libraryID int64, root string) error {
	if err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return w.add(path, libraryID)
		}

		return nil
	}); err != nil {
		// Drop the partially-registered directory set: roots was never recorded,
		// so a later reconcile would re-add the same dirs without ever removing
		// these (a bounded but pointless leak).
		w.unwatch(libraryID)

		return fmt.Errorf("walk watch root %s: %w", root, err)
	}
	w.mu.Lock()
	w.roots[libraryID] = root
	w.mu.Unlock()

	return nil
}

// unwatch removes every directory watched for libraryID and cancels its pending
// debounce timer, so a since-deleted library never drives a sync.
func (w *watcher) unwatch(libraryID int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for dir, id := range w.dirs {
		if id == libraryID {
			_ = w.fsw.Remove(dir)
			delete(w.dirs, dir)
		}
	}
	if t, ok := w.timers[libraryID]; ok {
		t.Stop()
		delete(w.timers, libraryID)
	}
	delete(w.roots, libraryID)
}

// watchedLibraryCount reports how many libraries are currently watched.
func (w *watcher) watchedLibraryCount() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.roots)
}

func (w *watcher) add(dir string, libraryID int64) error {
	if err := w.fsw.Add(dir); err != nil {
		return fmt.Errorf("watch directory %s: %w", dir, err)
	}
	w.mu.Lock()
	w.dirs[dir] = libraryID
	w.mu.Unlock()

	return nil
}

// run consumes events until Close.
func (w *watcher) run() {
	go func() {
		for {
			select {
			case <-w.stop:
				return
			case ev, ok := <-w.fsw.Events:
				if !ok {
					return
				}
				w.handle(ev)
			case err, ok := <-w.fsw.Errors:
				if !ok {
					return
				}
				w.log.Error("fsnotify", slog.Any("error", err))
			}
		}
	}()
}

func (w *watcher) handle(ev fsnotify.Event) {
	libraryID := w.libraryFor(filepath.Dir(ev.Name))
	if libraryID == 0 {
		return
	}
	// Watch newly created subdirectories so their contents are seen too. Files
	// created inside the new directory before the Add below lands are missed by
	// fsnotify — harmless: schedule() below debounces into a full library Sync,
	// and the folder parser always walks the whole tree.
	if ev.Op&fsnotify.Create != 0 {
		if info, err := os.Stat(ev.Name); err == nil && info.IsDir() {
			if err := w.add(ev.Name, libraryID); err != nil {
				w.log.Error("watch new directory", slog.String("path", ev.Name), slog.Any("error", err))
			}
		}
	}
	w.schedule(libraryID)
}

func (w *watcher) libraryFor(dir string) int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.dirs[dir]
}

// schedule (re)arms the debounce timer for a library.
func (w *watcher) schedule(libraryID int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if t, ok := w.timers[libraryID]; ok {
		t.Reset(w.debounce)
		return
	}
	w.timers[libraryID] = time.AfterFunc(w.debounce, func() {
		w.trigger(libraryID)
	})
}
