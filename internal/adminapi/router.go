package adminapi

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/zjutjh/jxh-go/internal/auth"
)

type RouteOptions struct {
	Public     bool
	Mutation   bool
	CSRF       bool
	Permission auth.Permission
}

type route struct {
	options RouteOptions
	handler http.Handler
}

type routeGroup struct {
	routes map[string]route
	mw     *middleware
}

func (g *routeGroup) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	entry, ok := g.routes[r.Method]
	if !ok {
		methods := make([]string, 0, len(g.routes))
		for method := range g.routes {
			methods = append(methods, method)
		}
		sort.Strings(methods)
		w.Header().Set("Allow", strings.Join(methods, ", "))
		writeAPIError(w, r, http.StatusMethodNotAllowed, CodeMethodNotAllowed, "请求方法不受支持", nil, false)
		return
	}
	g.mw.route(entry.options, entry.handler).ServeHTTP(w, r)
}

type Router struct {
	mux     *http.ServeMux
	mw      *middleware
	groups  map[string]*routeGroup
	handler http.Handler
}

func NewRouter(options MiddlewareOptions) (*Router, error) {
	mw, err := newMiddleware(options)
	if err != nil {
		return nil, err
	}
	router := &Router{mux: http.NewServeMux(), mw: mw, groups: make(map[string]*routeGroup)}
	router.mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeAPIError(w, r, http.StatusNotFound, CodeNotFound, "资源不存在", nil, false)
	})
	router.handler = mw.base(router.mux)
	return router, nil
}

func (r *Router) Handle(method, pattern string, options RouteOptions, handler http.Handler) error {
	if method == "" || pattern == "" || handler == nil {
		return fmt.Errorf("invalid admin route")
	}
	group := r.groups[pattern]
	if group == nil {
		group = &routeGroup{routes: make(map[string]route), mw: r.mw}
		r.groups[pattern] = group
		r.mux.Handle(pattern, group)
	}
	if _, exists := group.routes[method]; exists {
		return fmt.Errorf("duplicate admin route")
	}
	if options.Public && (options.CSRF || options.Permission != "") {
		return fmt.Errorf("public route cannot require CSRF or permission")
	}
	if options.CSRF && !options.Mutation {
		return fmt.Errorf("CSRF route must be mutating")
	}
	group.routes[method] = route{options: options, handler: handler}
	return nil
}

func (r *Router) HandleFunc(method, pattern string, options RouteOptions, handler http.HandlerFunc) error {
	return r.Handle(method, pattern, options, handler)
}

func (r *Router) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	r.handler.ServeHTTP(w, request)
}
