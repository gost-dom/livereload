# Livereload

Middleware providing live-reload capabilities for Go web development

## Intended projects

This works beautifully for web applications with server-side rendering.
Including those using hypermedia frameworks like HTMX or Datastar.

This is not a good fit for SPAs. Those front-end frameworks typically have
better solutions in their ecosystems with hot-module reloading.

## Low-dependency 

I always try to keep dependencies to a minimum. E.g., the library has its own
websocket implementation. It's not a full implementation, only enough for this
problem (e.g., this cannot handle messages larger than a single frame can hold)

The only dependency is [fsnotify] used for monitoring file system events.

I may refactor this in a way that the watcher gets moved to its own package, and
the watcher to use is plugged into the main `Livereload` type. This would allow
the core middleware to be a zero-dependency module, and client code can bring
their own trigger logic.

[fsnotify]: github.com/fsnotify/fsnotify

## How does this differentiate from other solutions.

The one tool ubiquitously mentioned to solve this problem in Go is [Air]. I find
this to be an overly complex solution, it's difficult and unintuitive to
configure, e.g., often the server was recompiled when unnecessary; often it
wasn't recompiled when it was necessary.

IMHO, this build on much simpler 

[Air]: https://github.com/air-verse/air

## How this works

The library has an HTTP middleware, you use to wrap your existing http Handler.

The middleware automatically injects a `<script src="..."></script>` element
into HTML responses (only affects responses with a `Content-Type: text/html`
header. The middleware also serves the necessary script, and accepts websocket
connections initiated by the script.

The application must call `WatchDir` for each directory with content to watch.
The watcher will push a `reload` message to the client, when changes are
detected, triggering a browser reload.

The script will also reload after the server restarts. It reacts to a broken
websocket, bringing the script into a _reconnect_ phase, and once a connection
can be established again, it reloads. The _reconnect_ phase prevents reloading
too soon, before the web server is ready to accept incoming connections,
resulting in a "connection failed" state in the browser.

## Avoiding dev-code in production builds.

If your project follows the [Server Project] module layout, you could
potentially create multiple `package main`, one for production, one for dev.

If you decouple _application logic_ (including HTTP handlers) from _startup
code_, then `package main` can be a very thin package only for launcing the HTTP
server and potentially some environment-specific configuration.


```sh
./internal/web # root http handler
./cmd/server # production server
./cmd/dev-server # development server
```

Production specific code in `./cmd/server` could include configuring logs to be
routed to an APM, and ellaborate server configuration settings. But the http
handler itself is from `./internal/web/`. 

Now `./cmd/dev-server` is another thin package, wrapping the handler in a live
reload middleware, and just calling `http.ListenAndServe`.

[Server Project]: https://go.dev/doc/modules/layout#server-project

### Alternate solution to avoid dev-server

I would assume you could use build tags to achieve the same goal, dev-code is
only used with a dev-code 

  - I find that using build-tags borders on what can be considered idiomatic.
    Good uses are when different platforms require different implementations. 
  - AFAIK, build tags affect entire files; Individual code lines adding
    middlewares cannot be conditionally compiled.
    - This means you need to alternate files with the same function.
  - It's less likely to accidentally start the wrong command, than to
    accidentally use the wrong build tags.

## Example

An example dev-server for the module `exampleapp` could look like this:

```go
// ./cmd/dev-server
package main

import (
    "net/http"
    "exampleapp/internal/web"
    "github.com/gost-dom/livereload"
)

func main() {
	var rootHandler http.Handler := web.createRootHttpHandler()
	var reloader livereload.Middleware
    // I've only tested on MacOS, but relative paths appear to be resolved from
    // the current working dir.
	err1 := reloader.WatchDir("static/css")
	err2 := reloader.WatchDir("static/js")
	if err := errors.Join(err1, err2); err != nil {
		slog.Errorf("Livereload initialization failed", "error", err)
	}
	http.ListenAndServer(":8080", reloader.Middleware(rootHandler))
}
```

### Off-topic: other uses for a dedicated dev-server command

The livereload is not the only reason for a dedicated dev-server; I may also
have test-specific endpoints here.

Refactoring CSS files can have unintended effects. To prevent this, I often have
regression tests rendering pages in a browser, taking screenshots, and comparing
them against a golden sample.

If there are changes in the rendered screenshot, the test breaks. If the changes
are intended, the actual screenshots gets elevated to a new golden sample.

To have identical output for each run, special endpoints render templates with
static data. And to simplify the test, those endpoints doesn't require
authentication, even when the content is only meant for authenticated users.

Neither the endpoints, nor test data, should exist in the production build. They
go into the dev-server.

## Restarting server automatically with nodemon

I'm not aware of a native Go-solution that can restart the server when source
code changes.

`nodemon` is a common tool for this problem in the node.js community. You can
install it with your favourite JS package manager, e.g., `npm i -g nodemon`

```sh
nodemon -e "go" -i "*_test.go" -x go --signal SIGTERM -- run ./cmd/dev-server
```

Explanation of the arguments

- `-e go` File extensions to watch
- `-i *_test.go` Don't restart when tests change
- `-x go` The name of the executable to run
- `--signal SIGTERM` I found this was the most reliable way to terminate `http.ListenAndServe`
- `--` Everything after is passed to the `go` executable, effectively becoming, `go run ./cmd/dev-server`

You can of course place this in a Taskfile or similar.

> [!WARNING]
> This only watches .go files, so content embedded with the `embed` package will
> not be taken into account. Adjust accordingly.

## Pro tip for templ and neovim: Program your editor to build templates

Building on top of the previous, automatically reloading when a .go file
changes, if you use templ, integrating live reload requires the following steps.

  1. Save a .templ file
  2. Run `templ generate {}.templ` producing 
  3. Nodemon (or your weapon of choice) reruns `go run ...`

Templ itself provides a watcher for building templ files into .go files, but
that's another process that needs to run, take up terminal space.

Another approach is to program your editor to just compile the templ file after
save. A benefit of this is that any compilation errors are easy to get in the
editor.

In neovim, you could do it with this script (adapt if you don't use `go tool`
for templ):

```lua
vim.api.nvim_create_autocmd("BufWritePost", {
  group = group,
  pattern = "*.templ",
  callback = function(event)
    local stdout = {}
    local stderr = {}
    local jobId = vim.fn.jobstart({ "go", "tool", "templ", "generate", "-f", event.file }, {
      stderr_buffered = true,
      stdout_buffered = true,
      on_stdout = function(_, data)
        stdout = data
      end,
      on_stderr = function(_, data)
        stderr = data
      end,
      on_exit = function(_, exitcode)
        if exitcode > 0 then
          print(vim.inspect({
            "Templ error",
            exitcode = exitcode,
            stdout = stdout,
            stderr = stderr,
          }))
        end
      end,
    })
  end,
})
```
