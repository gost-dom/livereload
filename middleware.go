package livereload

import (
	"bufio"
	"context"
	"crypto"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"path"
	"slices"
	"sync"

	"github.com/fsnotify/fsnotify"
)

type fsEvent = struct{}

type middleware struct {
	Handler  http.Handler
	config   Livereload
	watchers []chan fsEvent
	mu       sync.Mutex
}

func (m *middleware) start() error {
	go func() {
		for e := range m.config.watcher.Events {
			if e.Has(fsnotify.Create) || e.Has(fsnotify.Write) || e.Has(fsnotify.Rename) {
				for _, c := range m.getWatchers() {
					go func() { c <- fsEvent{} }()
				}
			}
		}
	}()
	go func() {
		for e := range m.config.watcher.Errors {
			m.logger().LogError("livereload: error reported by fsnotify", e)
		}
	}()
	return nil
}

func (m *middleware) createWatcher() chan fsEvent {
	// Create a buffered channel, as we don't want to block on sending.
	// Otherwise, this could potentially hang if a client disconnects while
	// notifying subscribers.
	c := make(chan fsEvent, 1)
	m.mu.Lock()
	defer m.mu.Unlock()
	m.watchers = append(m.watchers, c)
	return c
}

// getWatchers() return a clone of current subscribes, so we can minimize the
// time the lock is kept open.
func (m *middleware) getWatchers() []chan fsEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.watchers)
}

func (m *middleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ctx := context.Background()
	root := m.config.Path
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
	w = &scriptInjector{ResponseWriter: w, scriptPath: scriptPath, logger: m.logger()}
	m.Handler.ServeHTTP(w, r)
}

func (m *middleware) logger() *logger {
	return (*logger)(m.config.Logger)
}

func (m *middleware) removeChannel(c chan fsEvent) {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.watchers = slices.DeleteFunc(m.watchers, func(x chan fsEvent) bool { return x == c })
}

// sendWsHandshake transmist the WS handshake response
//
// https://developer.mozilla.org/en-US/docs/Web/API/WebSockets_API/Writing_WebSocket_servers#server_handshake_response
func (m *middleware) sendWsHandshake(rw *bufio.ReadWriter, r *http.Request) {
	rw.WriteString("HTTP/1.1 101 Switching Protocols\r\n")
	rw.WriteString("Upgrade: websocket\r\n")
	rw.WriteString("Connection: Upgrade\r\n")
	rw.WriteString("Sec-WebSocket-Accept: ")
	rw.WriteString(calculateSecWebSocketAccept(r))
	rw.WriteString("\r\n\r\n")
	rw.Flush()
}

func (m *middleware) waitForCloseFrame(ctx context.Context, rw *bufio.ReadWriter) context.Context {
	ctx, cancel := context.WithCancel(ctx)
	go func() {
		b := make([]byte, 1000)
		for {
			i, err := rw.Read(b)
			if err != nil || i == 0 {
				if err != io.EOF {
					m.logger().LogError("livereload: error reading from websocket", err)
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

func (m *middleware) webSocket(ctx context.Context, w http.ResponseWriter, r *http.Request) {
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
