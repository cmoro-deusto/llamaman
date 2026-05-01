package server

import (
	"errors"
	"io"
	"os"

	"github.com/fsnotify/fsnotify"
)

// Tailer streams a log file from offset 0, then follows new writes via
// fsnotify. Output is delivered as raw chunks (not necessarily one line at
// a time); callers that want lines should split downstream.
//
// The returned channel closes when Close is called, when the watcher
// errors out, or when the file is removed.
type Tailer struct {
	path    string
	watcher *fsnotify.Watcher
	file    *os.File
	out     chan string
	done    chan struct{}
}

// NewTailer opens path read-only and starts a background watcher. The file
// must exist; the spawn path always creates it before tailing begins.
func NewTailer(path string) (*Tailer, error) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		w.Close()
		return nil, err
	}
	if err := w.Add(path); err != nil {
		f.Close()
		w.Close()
		return nil, err
	}
	t := &Tailer{
		path:    path,
		watcher: w,
		file:    f,
		out:     make(chan string, 256),
		done:    make(chan struct{}),
	}
	go t.run()
	return t, nil
}

// Chunks returns a receive-only channel of file contents. Closed when the
// tailer terminates.
func (t *Tailer) Chunks() <-chan string { return t.out }

// Close stops the watcher and closes the file. Safe to call multiple times.
func (t *Tailer) Close() {
	select {
	case <-t.done:
	default:
		close(t.done)
	}
}

func (t *Tailer) run() {
	defer close(t.out)
	defer t.watcher.Close()
	defer t.file.Close()

	t.drain() // initial read of any pre-existing content

	for {
		select {
		case <-t.done:
			return
		case ev, ok := <-t.watcher.Events:
			if !ok {
				return
			}
			if ev.Op&fsnotify.Write != 0 {
				t.drain()
			}
			if ev.Op&(fsnotify.Remove|fsnotify.Rename) != 0 {
				return
			}
		case _, ok := <-t.watcher.Errors:
			if !ok {
				return
			}
			// Best-effort: log via slog upstream if needed; keep tailing.
		}
	}
}

func (t *Tailer) drain() {
	buf := make([]byte, 4096)
	for {
		n, err := t.file.Read(buf)
		if n > 0 {
			select {
			case t.out <- string(buf[:n]):
			case <-t.done:
				return
			}
		}
		if errors.Is(err, io.EOF) || err == nil && n == 0 {
			return
		}
		if err != nil {
			return
		}
	}
}
