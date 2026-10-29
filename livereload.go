package livereload

import (
	"fmt"
	"log/slog"
	"net/http"
	"path"
	"runtime"

	"github.com/fsnotify/fsnotify"
)

const defaultPath = "/_reload"

// Livereload struct is used to configure reloading before creating middlewares
//
//   - Add directories to watch with WatchDir
//   - Set Path to override default endpoints
//   - Set an *slog.Logger for centralised logging
//
// When configured, call [*Livereload.Middleware] attach to an http.Handler
//
// Create a [Livereload] that can intercept HTTP requests. Add directories to
// watch using [*Livereload.WatchDir]. Call [*Livereload.Middleware] to create
// the middleware based on the configuration.
//
// # Example usage
//
// A typical use case would require monitoring CSS and JS files. In this
// example, we ignore errors - the server can still run; it just lacks live-reload
// capabilities.
//
//	func main() {
//		var reloader livereload.Livereload
//		err1 := reloader.WatchDir("static/css")
//		err2 := reloader.WatchDir("static/js")
//		if err := errors.Join(err1, err2); err != nil {
//			// Up to you, if failing to watch dirs should fail early, or merely
//			// log. The server should still work normally after all, just the
//			// reload that will not work.
//			slog.Error("Livereload initialization failed", "err", err)
//		}
//		rootHandler := createRootHttpHandler()
//		http.ListenAndServe(":8080", reloader.Middleware(rootHandler))
//	}
//
// # Do not combine with StripPrefix
//
// This middleware depends on requests paths being the original browser request
// paths. It will not work with StripPrefix, or other path-mutating middlewares.
// The script would fail to connect to the websocket
type Livereload struct {
	// The base path where the script and websocket is served. Defaults to /_reload
	//
	// The script monitors the following paths:
	//
	// 	- /_reload/script.js - Client script to be injected
	// 	- /_reload/ws        - Websocket the script connects to
	Path string
	// In case of abnormal websocket errors, they will be reported to Logger if set; otherwise they are written to os.Stderr.
	//
	// This behaviour could change based on user feedback
	Logger  *slog.Logger
	watcher *fsnotify.Watcher
}

var rootDir string

func init() {
	_, p, _, _ := runtime.Caller(0)
	rootDir = path.Dir(p)
}

// Middleware has the common middleware format, a function accepting an
// http.Handler, and returns a new handler
func (m *Livereload) Middleware(h http.Handler) http.Handler {
	m.ensureWatcher()
	res := &middleware{
		Handler: h,
		config:  *m,
	}
	res.start()
	return res
}

func (m *Livereload) ensureWatcher() error {
	if m.watcher == nil {
		var err error
		if m.watcher, err = fsnotify.NewWatcher(); err != nil {
			return fmt.Errorf("livereload: error creating watcher: %w", err)
		}
	}
	return nil
}

// WatchDir add
//
// Client code should call WatchDir before creating the middleware. In the
// current version, calling WatchDir after does work, but this may be subject to
// change.
func (m *Livereload) WatchDir(p string) error {
	if err := m.ensureWatcher(); err != nil {
		return err
	}
	if err := m.watcher.Add(p); err != nil {
		return fmt.Errorf("%s: livereload: %w", p, err)
	}
	return nil
}
