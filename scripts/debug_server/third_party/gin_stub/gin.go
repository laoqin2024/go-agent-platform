package gin

import (
	"encoding/json"
	"net/http"
)

// H is a shortcut for map used by Gin.
type H map[string]any

// HandlerFunc is the handler used by this stub.
type HandlerFunc func(*Context)

// Context is a minimal subset of gin.Context required by debug_server.
type Context struct {
	Writer  http.ResponseWriter
	Request *http.Request
}

// ShouldBindJSON decodes JSON from request body.
func (c *Context) ShouldBindJSON(obj any) error {
	defer func() {
		_ = c.Request.Body.Close()
	}()
	return json.NewDecoder(c.Request.Body).Decode(obj)
}

// JSON writes obj as JSON with status code.
func (c *Context) JSON(statusCode int, obj any) {
	c.Writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	c.Writer.WriteHeader(statusCode)
	_ = json.NewEncoder(c.Writer).Encode(obj)
}

// Engine is a minimal HTTP router.
type Engine struct {
	mux *http.ServeMux
}

// Default returns a new Engine.
func Default() *Engine {
	return &Engine{mux: http.NewServeMux()}
}

func (e *Engine) add(method string, path string, handler HandlerFunc) {
	e.mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != method {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		c := &Context{Writer: w, Request: r}
		handler(c)
	})
}

func (e *Engine) POST(path string, handler HandlerFunc) {
	e.add(http.MethodPost, path, handler)
}

func (e *Engine) GET(path string, handler HandlerFunc) {
	e.add(http.MethodGet, path, handler)
}

// StaticFile serves a single file at the given route.
func (e *Engine) StaticFile(path string, file string) {
	e.mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, file)
	})
}

// Run starts the HTTP server.
func (e *Engine) Run(addr ...string) error {
	address := ":8080"
	if len(addr) > 0 && addr[0] != "" {
		address = addr[0]
	}
	return http.ListenAndServe(address, e.mux)
}
