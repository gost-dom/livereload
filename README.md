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

I may refactor this in a way that fsnotify-dependent code is moved to its own module, 
and you need to inject a watcher. Thus leaving the main livereload module free
of dependencies. But I don't have plans for this yet.

[fsnotify]: https://github.com/fsnotify/fsnotify

## How does this differentiate from other solutions.

The one tool ubiquitously mentioned to solve this problem in Go is [Air]. But I
find Air to be unintuitive, overly complex to configure, builds unnecessary
artefacts (when you can launch with `go run`), and starts an unnecessary proxy
server, adding one extra potential port conflict.

IMHO, this build on much simpler principles.

[Air]: https://github.com/air-verse/air

## How this works

The library has an HTTP middleware, you use to wrap your existing http Handler.

The middleware automatically injects a `<script src="..."></script>` element
into responses with a `Content-Type: text/html` header. The middleware also
serves the necessary script, and accepts websocket connections initiated by the
script.

The application must call `WatchDir` for each directory with content to watch;
they are not watched recursively. The watcher will push a `reload` message to
the client when changes are detected; and the script will reload the current URL.

The script also handles server restarts. When the websocket is broken, the
script goes into a _reconnect_ phase. Once a connection is established again, it
reloads. The _reconnect_ phase prevents reloading before the web server is ready
to accept incoming connections, resulting in a "connection failed" state in the
browser (requiring manual reload)

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
	var reloader livereload.Middleware
	err1 := reloader.WatchDir("static/css")
	err2 := reloader.WatchDir("static/js")
	if err := errors.Join(err1, err2); err != nil {
		slog.Errorf("Livereload initialization failed", "error", err)
	}
	rootHandler := web.createRootHttpHandler()
	http.ListenAndServer(":8080", reloader.Middleware(rootHandler))
}
```

If directories aren't absolute, they are resolved from the current working
directory (I've only tested on MacOS).

## Avoiding dev-code in production builds.

The previous example adds live reload behaviour into server executable. There
are at least two strategies to avoid this:

  - Use build tags, so livereload functionality is only added if a dev-tag is used.
  - Create separate `./cmd/server` and `./cmd/dev-server` packages, each being a
    `main` package, launching an HTTP server.

Whether the latter may not be a good fit for all projects, but works well for
projects that follow the the [Server Project] module layout.

By decoupling _application logic_ (including HTTP handlers) from _startup code_,
`package main` can be a very thin package with minimal responsibilities, e.g.
starting the HTTP listener and handling environment-specific configuration.


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

### Off-topic: other uses for a dedicated dev-server command

The livereload is not the only reason for a dedicated dev-server; I often have
test-specific endpoints for regression testinging templates.

Refactoring CSS files can have unintended effects. To prevent this, I often have
regression tests rendering pages in a browser, taking screenshots, and comparing
them against a golden sample.

If there are changes in the rendered screenshot, the test breaks. If the changes
are intended, the current screenshots are elevated to a new golden sample.

To have identical output for each run, special endpoints render templates with
static viewmodels. They also render for unauthenticated users, no matter the
intended use for the templates.

Those endpoints, as well as the static viewmodels, only exist in the dev-server.

## Restarting server automatically with nodemon

`nodemon` is a common tool for this problem in the node.js community. You can
install it with your favourite JS package manager, e.g., `npm i -g nodemon`

```sh
nodemon -e "go" -i "*_test.go" -x go --signal SIGTERM -- run ./cmd/dev-server
```

> [!TIP]
> You can of course place this in a Taskfile or similar.

> [!NOTE]
> This basically tells nodemon to run `go run ./cmd/dev-server`, and restart it
> whenever a `.go` file changes, but not when a test file changes. To shutdown
> the running server, it should send a `SIGTERM` signal. That signal is used
> here, as `http.ListenAndServe` terminates on that signal. If you have custom
> signal handling, you may want to adapt.

> [!WARNING]
> This only watches .go files, so content embedded with the `embed` package will
> not be taken into account. Adjust accordingly.

## Pro tip for templ and neovim: Program your editor to build .templ files

Building on top of the previous, automatically reloading when a .go file
changes, if you use templ, integrating live reload requires the following steps.

  1. Save a .templ file
  2. Run `templ generate -f {filename}.templ` to produce to `.go` file.
  3. Nodemon (or your tool of choice) reruns `go run ...`

Templ itself provides a watcher for building templ files into .go files, but I
prefer to just let my editor run templ automatically.

Pros of this approach is

  - It's not another process running in another shell
  - I rarely notice templ build errors, as the shell isn't normally visible on screen.
  - When the editor runs `templ generate`, it can detect non-zero exit codes,
    and present error messages to the user.

The following neovim script will implement that behaviour (adapt if you don't
use `go tool` for templ):

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
