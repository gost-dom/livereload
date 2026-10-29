// package livereload injects live reloading capabilities into web applications
// for faster feedback during development.
//
// # Intended use case
//
// This works well for server-rendered HTML, e.g. when combined with hypermedia
// frameworks like HTMX or Datastar.
//
// For SPA front-ends, like React/Angular/Vue, the solutions in their respective
// ecosystems work well, as they have the capabilities to hot reload individual
// components.
//
// Many SPAs also lose essential state if you reload (don't ask my opinion about
// those who write those solutions though).
//
// # How it works
//
// The package has a single exported type, [Livereload] for configuring and
// creating a middleware. The middleware does the following
//
//   - Injects a <script src="..."></script> into responses with Content-Type: text/html.
//   - Serves the required script
//   - Accepts a websocket connection
//   - Watches directories for modifications, pushing reload messages to the websocket
//   - The script reloads on reload messages
//   - The script reacts to broken connection on server shutdown, and reloads once the server is running again.
//
// To reload on Go code changes, you just need to automate server restart.
//
// # Usage with nodemon
//
// Nodemon is a popular tool in the node.js community for restarting a server
// when source code changes. It also works with non-js projects. Example usage,
// adapt main package to where you have your main package
//
//	nodemon -e "go" -i "*_test.go" -x go --signal SIGTERM -- run ./cmd/server
package livereload
