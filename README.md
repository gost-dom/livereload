# Livereload

Middleware providing live-reload capabilities for Go web development

## Intended projects

This works beautifully for web applications with server-side rendering.
Including those using hypermedia frameworks like HTMX or Datastar.

This is not a good fit for SPAs. Those front-end frameworks typically have
better solutions in their ecosystems with hot-module reloading.

## How does this differentiate from other solutions.

Other solutions I've tried includes Templ's own livereload capabilities, as well
as Air. They both require an extra "proxy server", a dedicated process, often
running in the background. And air is simply too complicated to configure. This
is a simple problem, the default configuration shouldn't be complex.

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

## But I don't want this in production code!?!

You don't have to. Make `package main` a thin package, decoupled from all
application logic. Only production specific code is in this package.

An example following the [Server Project]:

```sh
./internal/web # root http handler
./cmd/server # production server
./cmd/dev-server # development server
```

Package `./cmd/server` may configure logging with APM systems, and ellaborate
server configuration settings, but the http handler itself is from
`./internal/web/`. 

Now `./cmd/dev-server` is another thin package, wrapping the handler in a live
reload middleware, and just calling `http.ListenAndServe`.

[Server Project]: https://go.dev/doc/modules/layout#server-project

## Restarting server automatically with nodemon

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
