package rag

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/sipeed/picoclaw/pkg/logger"
)

const (
	defaultReindexDebounce = 2 * time.Second
	defaultFlushDebounce   = 30 * time.Second
)

// Watcher monitors kbRoot for file changes and triggers re-indexing
// with two debounce tiers:
//   - short debounce (2s): rebuilds in-memory indexes via BuildInMemory
//   - long debounce (30s): flushes to disk via Flush, clearing dirty flag
//
// If the process exits between reindex and flush, the dirty flag in bbolt
// ensures a full rebuild on next startup.
type Watcher struct {
	svc     *Service
	kbRoot  string
	fsw     *fsnotify.Watcher
	cancel  context.CancelFunc
	wg      sync.WaitGroup

	reindexDebounce time.Duration
	flushDebounce   time.Duration
}

// WatcherOption configures the watcher.
type WatcherOption func(*Watcher)

func WithReindexDebounce(d time.Duration) WatcherOption {
	return func(w *Watcher) { w.reindexDebounce = d }
}

func WithFlushDebounce(d time.Duration) WatcherOption {
	return func(w *Watcher) { w.flushDebounce = d }
}

// NewWatcher creates a file watcher for the given service's kbRoot.
func NewWatcher(svc *Service, opts ...WatcherOption) (*Watcher, error) {
	fsw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}

	w := &Watcher{
		svc:             svc,
		kbRoot:          svc.kbRoot,
		fsw:             fsw,
		reindexDebounce: defaultReindexDebounce,
		flushDebounce:   defaultFlushDebounce,
	}
	for _, opt := range opts {
		opt(w)
	}

	if err := addRecursive(fsw, w.kbRoot); err != nil {
		fsw.Close()
		return nil, err
	}

	return w, nil
}

// Start begins watching in background goroutines. The returned context
// controls the watcher lifecycle.
func (w *Watcher) Start(ctx context.Context) {
	ctx, w.cancel = context.WithCancel(ctx)
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		w.loop(ctx)
	}()
}

// Stop cancels the watcher and waits for all goroutines to finish.
func (w *Watcher) Stop() {
	if w.cancel != nil {
		w.cancel()
	}
	w.wg.Wait()
	w.fsw.Close()
}

func (w *Watcher) loop(ctx context.Context) {
	var reindexTimer *time.Timer
	var flushTimer *time.Timer

	resetReindex := func() {
		if reindexTimer == nil {
			reindexTimer = time.NewTimer(w.reindexDebounce)
		} else {
			if !reindexTimer.Stop() {
				select {
				case <-reindexTimer.C:
				default:
				}
			}
			reindexTimer.Reset(w.reindexDebounce)
		}
	}

	resetFlush := func() {
		if flushTimer == nil {
			flushTimer = time.NewTimer(w.flushDebounce)
		} else {
			if !flushTimer.Stop() {
				select {
				case <-flushTimer.C:
				default:
				}
			}
			flushTimer.Reset(w.flushDebounce)
		}
	}

	reindexC := func() <-chan time.Time {
		if reindexTimer == nil {
			return nil
		}
		return reindexTimer.C
	}

	flushC := func() <-chan time.Time {
		if flushTimer == nil {
			return nil
		}
		return flushTimer.C
	}

	for {
		select {
		case <-ctx.Done():
			w.flushIfDirty(context.Background())
			return

		case ev, ok := <-w.fsw.Events:
			if !ok {
				return
			}
			if !isRelevantEvent(ev) {
				continue
			}
			if ev.Has(fsnotify.Create) {
				if info, err := os.Stat(ev.Name); err == nil && info.IsDir() {
					_ = addRecursive(w.fsw, ev.Name)
				}
			}
			resetReindex()
			resetFlush()

		case err, ok := <-w.fsw.Errors:
			if !ok {
				return
			}
			logger.Warn(fmt.Sprintf("rag watcher error: %v", err))

		case <-reindexC():
			reindexTimer = nil
			w.reindex(ctx)

		case <-flushC():
			flushTimer = nil
			w.flushIfDirty(ctx)
		}
	}
}

func (w *Watcher) reindex(ctx context.Context) {
	fp, ok := w.svc.provider.(FlushableProvider)
	if !ok {
		if _, err := w.svc.BuildIndex(ctx); err != nil {
			logger.Warn(fmt.Sprintf("rag watcher reindex: %v", err))
		}
		return
	}

	// Chunking is stateless IO — no semaphore needed.
	chunks, info, err := w.svc.buildChunksAndInfo(ctx)
	if err != nil {
		logger.Warn(fmt.Sprintf("rag watcher build chunks: %v", err))
		return
	}

	// Only the in-memory index mutation needs the concurrency slot.
	if err := w.svc.acquireSem(ctx); err != nil {
		logger.Warn(fmt.Sprintf("rag watcher reindex: %v", err))
		return
	}
	defer w.svc.releaseSem()

	if err := fp.BuildInMemory(ctx, chunks, *info); err != nil {
		logger.Warn(fmt.Sprintf("rag watcher reindex in-memory: %v", err))
	}
	logger.Info(fmt.Sprintf("rag watcher: reindexed %d chunks (dirty, flush pending)", len(chunks)))
}

func (w *Watcher) flushIfDirty(ctx context.Context) {
	fp, ok := w.svc.provider.(FlushableProvider)
	if !ok || !fp.IsDirty() {
		return
	}
	// Guard with a timeout so a slow disk can't hang shutdown.
	done := make(chan error, 1)
	go func() { done <- fp.Flush() }()
	select {
	case err := <-done:
		if err != nil {
			logger.Warn(fmt.Sprintf("rag watcher flush: %v", err))
			return
		}
		logger.Info("rag watcher: flushed index to disk")
	case <-ctx.Done():
		logger.Warn("rag watcher flush: timed out")
	}
}

func isRelevantEvent(ev fsnotify.Event) bool {
	if ev.Has(fsnotify.Chmod) && !ev.Has(fsnotify.Write) && !ev.Has(fsnotify.Create) && !ev.Has(fsnotify.Remove) {
		return false
	}
	ext := strings.ToLower(filepath.Ext(ev.Name))
	return ext == ".md"
}

func addRecursive(fsw *fsnotify.Watcher, root string) error {
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if err := fsw.Add(path); err != nil {
				return nil
			}
		}
		return nil
	})
}
