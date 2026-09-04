package api

import "strings"

// The generated data API's routes are defined at runtime by an administrator,
// so they cannot be registered on net/http's mux at startup. This file
// implements a small matcher over the compiled endpoint list instead.

// compiledRoute is one matchable route.
type compiledRoute struct {
	endpoint  Endpoint
	operation CRUDOperation
	segments  []segment
}

// segment is one path component: either a literal or a {name} capture.
type segment struct {
	literal string
	param   string // non-empty when this segment captures a value
}

// router matches a method and path onto an endpoint.
type router struct {
	// routes are grouped by method, then ordered so that more specific routes
	// are tried first.
	byMethod map[string][]compiledRoute
}

func newRouter(endpoints []Endpoint) *router {
	r := &router{byMethod: make(map[string][]compiledRoute)}
	for _, e := range endpoints {
		if !e.Enabled {
			continue
		}
		for _, route := range e.Routes() {
			r.byMethod[route.Method] = append(r.byMethod[route.Method], compiledRoute{
				endpoint:  e,
				operation: route.Operation,
				segments:  compileSegments(route.Path),
			})
		}
	}

	// Literal segments beat captures, so "/products/search" is not swallowed by
	// "/products/{id}".
	for method := range r.byMethod {
		routes := r.byMethod[method]
		insertionSort(routes, func(a, b compiledRoute) bool {
			if len(a.segments) != len(b.segments) {
				return len(a.segments) < len(b.segments)
			}
			for i := range a.segments {
				aParam := a.segments[i].param != ""
				bParam := b.segments[i].param != ""
				if aParam != bParam {
					return !aParam
				}
			}
			return false
		})
	}
	return r
}

func compileSegments(path string) []segment {
	trimmed := strings.Trim(path, "/")
	if trimmed == "" {
		return nil
	}
	parts := strings.Split(trimmed, "/")
	out := make([]segment, 0, len(parts))
	for _, p := range parts {
		if m := pathParamRe.FindStringSubmatch(p); m != nil {
			out = append(out, segment{param: m[1]})
			continue
		}
		out = append(out, segment{literal: p})
	}
	return out
}

// match finds the endpoint serving a request, along with any captured path
// parameters.
func (r *router) match(method, path string) (*compiledRoute, map[string]string, bool) {
	candidates, ok := r.byMethod[method]
	if !ok {
		return nil, nil, false
	}

	trimmed := strings.Trim(path, "/")
	var parts []string
	if trimmed != "" {
		parts = strings.Split(trimmed, "/")
	}

	for i := range candidates {
		route := &candidates[i]
		if len(route.segments) != len(parts) {
			continue
		}
		params := map[string]string{}
		matched := true
		for j, seg := range route.segments {
			if seg.param != "" {
				params[seg.param] = parts[j]
				continue
			}
			if seg.literal != parts[j] {
				matched = false
				break
			}
		}
		if matched {
			return route, params, true
		}
	}
	return nil, nil, false
}

// methodsFor reports which methods are defined for a path, so a mismatched
// method can be answered with 405 and an Allow header rather than a bare 404.
func (r *router) methodsFor(path string) []string {
	trimmed := strings.Trim(path, "/")
	var parts []string
	if trimmed != "" {
		parts = strings.Split(trimmed, "/")
	}

	var methods []string
	for method, candidates := range r.byMethod {
		for i := range candidates {
			route := &candidates[i]
			if len(route.segments) != len(parts) {
				continue
			}
			matched := true
			for j, seg := range route.segments {
				if seg.param == "" && seg.literal != parts[j] {
					matched = false
					break
				}
			}
			if matched {
				methods = append(methods, method)
				break
			}
		}
	}
	return methods
}

// insertionSort keeps the ordering stable without pulling in a comparator
// adapter; route lists are short.
func insertionSort(routes []compiledRoute, less func(a, b compiledRoute) bool) {
	for i := 1; i < len(routes); i++ {
		for j := i; j > 0 && less(routes[j], routes[j-1]); j-- {
			routes[j], routes[j-1] = routes[j-1], routes[j]
		}
	}
}
