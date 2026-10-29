package livereload

import (
	"bufio"
	"bytes"
	"context"
	"crypto"
	"encoding/base64"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"

	"github.com/fsnotify/fsnotify"
)

type event = struct{}

const replacementPoint = "</body>"

var replacementPointFirstChar = replacementPoint[0]
var replacementPointLastChar = replacementPoint[len(replacementPoint)-1]

const defaultPath = "/_reload"

// Middleware wraps an existing http.Handler and injects live-reload
// capabilities into the response.
//
// Create a [Middleware] that can intercept HTTP requests. Add directories to
// watch using [*Middleware.WatchDir], and start watching for changes with
// [*Middleware.Start]
//
// As an example:
//
//	func main() {
//		var rootHandler http.Handler := createRootHttpHandler()
//		reloader := livereload.Middleware{Handler: rootHandler}
//		err1 := reloader.WatchDir("static/css")
//		err2 := reloader.WatchDir("static/js")
//		err3 := reloader.Watch()
//		if err := errors.Join(err1, err2, err3); err != nil {
//			// Up to you, if failing to watch dirs should fail early, or merely
//			// log. The server should still work normally after all, just the
//			// reload that will not work.
//			slog.Errorf("Livereload initialization failed", "error", err)
//		}
//		http.ListenAndServer(":8080", reloader)
//	}
//
// # Websocket errors
//
// livereload reads from the websocket channel. If the read fails, the error is
// logged.
//
// You can pass an *slog.Logger instance to the middleware, which will receive
// an Error record. Otherwise, the error is logged to os.Stderr
//
// This behaviour could change based on user feedback.
type Middleware struct {
	Handler http.Handler
	// A path where the script and websocket is served. Defaults to /_reload
	//
	// - /_reload/script.js - Client script to force reload
	// - /_reload/ws 		- Web socket path for reloading
	Path     string
	Logger   *slog.Logger
	watcher  *fsnotify.Watcher
	watchers []chan event

	goWatcher *fsnotify.Watcher
	server    *http.Server

	mu sync.Mutex
}

func NewMiddleware(h http.Handler) *Middleware { return &Middleware{Handler: h} }

var rootDir string

func init() {
	_, p, _, _ := runtime.Caller(0)
	rootDir = path.Dir(p)
}

func (m *Middleware) start() error {
	go func() {
		for e := range m.watcher.Events {
			if e.Has(fsnotify.Create) || e.Has(fsnotify.Write) || e.Has(fsnotify.Rename) {
				for _, c := range m.getWatchers() {
					c <- event{}
				}
			}
		}
	}()
	return nil
}

func (m *Middleware) WatchPath(p string) error {
	if m.watcher == nil {
		var err error
		if m.watcher, err = fsnotify.NewWatcher(); err != nil {
			return fmt.Errorf("livereload: error creating watcher: %w", err)
		}
		m.start()
	}
	if err := m.watcher.Add(p); err != nil {
		return fmt.Errorf("%s: livereload: %w", p, err)
	}
	return nil
}

func (m *Middleware) createWatcher() chan event {
	// Create a buffered channel, as we don't want to block on sending.
	// Otherwise, this could potentially hang if a client disconnects while
	// notifying subscribers.
	c := make(chan event, 1)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.watchers = append(m.watchers, c)
	return c
}

// getWatchers() return a clone of current subscribes, so we can minimize the
// time the lock is kept open.
func (m *Middleware) getWatchers() []chan event {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.watchers)
}

func (m *Middleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := context.Background()
	root := m.Path
	if root == "" {
		root = defaultPath
	}
	scriptPath := path.Join(root, "script.js")
	socketPath := path.Join(root, "ws")
	if r.URL.Path == scriptPath {
		w.Header().Add("Content-Type", "text/javascript")
		fmt.Fprint(w, createScript(socketPath))
		return
	}
	if r.URL.Path == socketPath {
		m.webSocket(ctx, w, r)
		return
	}
	w = &rewritingWriter{ResponseWriter: w, scriptPath: scriptPath}
	m.Handler.ServeHTTP(w, r)
}

// calculateSecWebSocketAccept returns value for the Sec-WebSocket-Accept
// response header based on the Sec-WebSocket-Key request header.
//
// The return value is a SHA1 hash of the request header value combined with the
// magic string, "258EAFA5-E914-47DA-95CA-C5AB0DC85B11". The magic string
// originates from the websocket specification.
func calculateSecWebSocketAccept(r *http.Request) string {
	key := r.Header.Get("Sec-WebSocket-Key")
	hash := crypto.SHA1.New()
	hash.Write([]byte(key))
	hash.Write([]byte("258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	return base64.StdEncoding.EncodeToString(hash.Sum(nil))
}

func (m *Middleware) removeChannel(c chan event) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.watchers = slices.DeleteFunc(m.watchers, func(x chan event) bool { return x == c })
}

// sendWsHandshake transmist the WS handshake response
//
// https://developer.mozilla.org/en-US/docs/Web/API/WebSockets_API/Writing_WebSocket_servers#server_handshake_response
func (m *Middleware) sendWsHandshake(rw *bufio.ReadWriter, r *http.Request) {
	rw.WriteString("HTTP/1.1 101 Switching Protocols\r\n")
	rw.WriteString("Upgrade: websocket\r\n")
	rw.WriteString("Connection: Upgrade\r\n")
	rw.WriteString("Sec-WebSocket-Accept: ")
	rw.WriteString(calculateSecWebSocketAccept(r))
	rw.WriteString("\r\n\r\n")
	rw.Flush()
}

func (m *Middleware) waitForCloseFrame(ctx context.Context, rw *bufio.ReadWriter) context.Context {
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		b := make([]byte, 1000)
		for {
			i, err := rw.Read(b)
			if err != nil || i == 0 {
				if err != io.EOF {
					if l := m.Logger; l != nil {
						l.Error("livereload: error reading from websocket", "err", err)
					} else {
						fmt.Fprintf(os.Stderr, "livereload: error reading from websocket: %v", err)
					}
				}
				cancel()
				return
			}
			// This is really, really stupid, but it's good enough for the problem.
			if b[0] == 0b_1000_1000 { // Close frame
				cancel()
				return
			}
		}
	}()
	return ctx
}

func (m *Middleware) webSocket(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	c := m.createWatcher()
	defer m.removeChannel(c)

	fmt.Println("Start socket")
	ctrl := http.NewResponseController(w)
	_, rw, err := ctrl.Hijack()
	if err != nil {
		fmt.Println("Error hijacking response", err)
		return
	}

	m.sendWsHandshake(rw, r)
	ctx = m.waitForCloseFrame(ctx, rw)

	for {
		select {
		case <-c:
			writeWsMessage(rw, "RELOAD")
			return
		case <-ctx.Done():
			fmt.Println("Context done")
			return
		}
	}
}

func writeWsMessage(w *bufio.ReadWriter, message string) error {
	b := []byte(message)
	l := len(b)

	// A message longer than 125 bytes require multi-byte length encoding
	if l > 125 {
		return fmt.Errorf(
			"Current implementation only supports messages of up to 125 bytes. Message length: %d",
			l,
		)
	}
	// bit 0: This is the final frame
	// bit 7: Text message
	// bit 8: Message is not encoded (prod-level security not necessary)
	// bits 9-15: Message length
	//
	// Because l is already checked to be < 125, bit 8 is not set.
	_, err := w.Write(append([]byte{0b_1000_0001, byte(l)}, b...))
	if err == nil {
		w.Flush()
	}
	return err
}

type rewritingWriter struct {
	http.ResponseWriter
	scriptPath       string
	firstByteWritten bool
	injected         bool
	isHtml           bool
	carry            []byte
}

func (w rewritingWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

var bodyEndTag = regexp.MustCompile(regexp.QuoteMeta(replacementPoint))

// Write implements http.ResponseWriter. It delegates most calls unmodified to
// the wrapped ResponseWriter. Only when the Content-Type header is text/html
// will it attempt to inject the reload script before the end </body> tag.
//
// The implementation will take into account that the end tag may not exist in
// its entirety in a single call, e.g., in one call to Write, buffer b could end
// with "</bo", while it starts with "dy>" in the subsequent call.
//
// Therefore, if the final 7 bytes, the length of "</body>" contains a "<", and
// doesn't subsequently contain a ">", only the part until "<" is passed to the
// wrapped ResponseWriter. The remaning bytes, which could potentially be the
// beginning of "</body>" is stored in a "carry" prepended to slice b in the
// next call to Write.
func (w *rewritingWriter) Write(b []byte) (int, error) {
	if !w.firstByteWritten {
		ct := w.ResponseWriter.Header().Get("Content-Type")
		w.isHtml = strings.HasPrefix(ct, "text/html")
		w.firstByteWritten = true
	}
	if !w.isHtml || w.injected {
		return w.ResponseWriter.Write(b)
	}
	l := len(b)
	b = append(w.carry, b...)
	w.carry = nil
	// We want to inject the script _before_ the </body> tag. If this is in
	// slice b, replace
	if found := bodyEndTag.FindIndex(b); found != nil {
		w.ResponseWriter.Write(b[:found[0]])
		fmt.Fprintf(w.ResponseWriter, `<script src="%s"></script>`, w.scriptPath)
		_, err := w.ResponseWriter.Write(b[found[0]:])
		w.injected = true
		return l, err
	}
	// Detect potential unfinished </body> end tag.
	const bodyLen = len(replacementPoint)
	tagStartIndex := bytes.LastIndexByte(b, replacementPointFirstChar)
	tagEndIndex := bytes.LastIndexByte(b, replacementPointLastChar)
	lastEndTagCouldBeBody := tagStartIndex >= len(b)-bodyLen
	lastEndTagCompleted := tagStartIndex < tagEndIndex
	if lastEndTagCouldBeBody && lastEndTagCompleted {
		w.carry = b[tagStartIndex:]
		b = b[:tagStartIndex]
	}
	_, err := w.ResponseWriter.Write(b)
	return l, err
}

func potentiallyContainsUnfinishedEndTag(b []byte) bool {
	const bodyLen = len("</body>")
	endTagStartIndex := bytes.LastIndex(b, []byte("</"))
	tagEndIndex := bytes.LastIndexByte(b, '>')
	return endTagStartIndex >= len(b)-bodyLen && endTagStartIndex > tagEndIndex
}

func createScript(socketPath string) string {
	return `
	const initialTimeout = 10
	console.log("livereload: script loaded")
	const ws = new WebSocket("` + socketPath + `");
	ws.addEventListener('open', e => { 
		console.log("livereload: Socket opened", ws.protocol) 
		ws.send("Hello from client\r\n")
	})
	ws.addEventListener('message', e => {
		console.log("livereload: message", e)
		if (e.data === "RELOAD") {
			window.location.reload()
		}
	})
	ws.addEventListener('close', e => { 
		console.log("livereload: connection closed", e)
		const retry = (timeout) => {
			console.log("livereload: checking for live server after", timeout)
			fetch("/").then(() => {
				console.log("livereload: server back online: reload")
				window.location.reload()
			}, () => {
				const newTimeout = timeout + initialTimeout
				setTimeout( () => { retry(newTimeout) }, newTimeout)
			})
		}
		setTimeout(() => { retry(initialTimeout) }, initialTimeout)
	})
	ws.addEventListener('error', e => { 
		console.error("livereload: socket error", e) 
	})
`
}
