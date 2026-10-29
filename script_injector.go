package livereload

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

// scriptInjector implements http.ResponseWriter by embedding an existing
// writer. The Write method is replaced with one that injects a script tag for
// HTML responses.
type scriptInjector struct {
	http.ResponseWriter
	scriptPath       string
	firstByteWritten bool
	injected         bool
	isHtml           bool
	carry            []byte
}

func (w scriptInjector) Unwrap() http.ResponseWriter { return w.ResponseWriter }

const replacementPoint = "</body>"

var replacementPointFirstChar = replacementPoint[0]
var replacementPointLastChar = replacementPoint[len(replacementPoint)-1]

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
func (w *scriptInjector) Write(b []byte) (int, error) {
	if !w.firstByteWritten {
		ct := w.ResponseWriter.Header().Get("Content-Type")
		w.isHtml = strings.HasPrefix(ct, "text/html")
		w.firstByteWritten = true
	}
	if !w.isHtml || w.injected {
		return w.ResponseWriter.Write(b)
	}
	l := len(b)
	b = w.addCarry(b) // Include data from last call
	// We want to inject the script _before_ the </body> tag. If this is in
	// slice b, call the wrapped Write, injecting the script before the end tag.
	if found := bodyEndTag.FindIndex(b); found != nil {
		_, err1 := w.ResponseWriter.Write(b[:found[0]])
		_, err2 := fmt.Fprintf(w.ResponseWriter, `<script src="%s"></script>`, w.scriptPath)
		_, err3 := w.ResponseWriter.Write(b[found[0]:])
		w.injected = true
		return l, errors.Join(err1, err2, err3)
	}
	// Detect potential unfinished </body> end tag.
	b, w.carry = splitAtPotentiallyPartialTag(b)
	_, err := w.ResponseWriter.Write(b)
	return l, err
}

func (w *scriptInjector) addCarry(b []byte) []byte {
	b = append(w.carry, b...)
	w.carry = nil
	return b
}

// splitAtPotentiallyPartialTag detects if the last part of the slice could
// potentially contain an unfinished </body> tag, e.g., it ends with "</".
//
// Return value newB contains the part that can safely be written, and carry
// contains the part that need to be compared with the next buffer.
func splitAtPotentiallyPartialTag(b []byte) (newB []byte, carry []byte) {
	const bodyLen = len(replacementPoint)
	tagStartIndex := bytes.LastIndexByte(b, replacementPointFirstChar)
	tagEndIndex := bytes.LastIndexByte(b, replacementPointLastChar)
	lastEndTagCouldBeBody := tagStartIndex >= len(b)-bodyLen
	lastEndTagCompleted := tagStartIndex < tagEndIndex
	if lastEndTagCouldBeBody && lastEndTagCompleted {
		return b[:tagStartIndex], b[tagStartIndex:]
	} else {
		return b, nil
	}
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
