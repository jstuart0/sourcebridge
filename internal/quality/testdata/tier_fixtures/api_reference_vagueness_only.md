# API Reference: Router Module

## Overview

The router module (internal/router/router.go:1-80) is the central dispatch layer for
incoming HTTP requests. It maps URL patterns to handler functions using a trie-based
matching algorithm (internal/router/trie.go:1-55). Each handler is registered once at
startup and the routing table is immutable during request processing.

## Request Dispatch

The `Dispatch` method (internal/router/router.go:42-68) accepts an HTTP request and
returns the matched handler along with any path parameters extracted from the URL.
When no route matches, the method returns the configured 404 handler
(internal/router/defaults.go:12-20). Route matching is case-insensitive by default.

Example usage:

```go
handler, params, err := r.Dispatch(req)
if err != nil {
    http.Error(w, err.Error(), http.StatusNotFound)
    return
}
handler.ServeHTTP(w, req.WithContext(params.IntoContext(req.Context())))
```

## Middleware Stack

Various middleware components can be attached to the router at initialization time.
The middleware chain is invoked in registration order for each matched route.
Multiple interceptors may inspect or modify the request before it reaches the final
handler. In some cases a middleware will short-circuit the chain by writing a response
directly without calling the next handler in the stack.

## Route Registration

The `Register` method (internal/router/router.go:15-38) accepts a pattern string and
a handler function. Patterns follow the syntax described in the router configuration
guide (internal/router/pattern.go:1-30). Duplicate patterns cause the registration
call to return an error. The method validates the pattern before storing it in the
routing table.
