package main

import (
	"context"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
)

type portKey struct{}

func withPort(r *http.Request, port int) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), portKey{}, port))
}

// newAppProxy forwards a request unchanged (path included: each app is built with base
// path /<key>) to host:<port from the request context>. When the app cannot be reached
// or answers 502-504 itself, down renders the project's info page instead.
func newAppProxy(host string, down http.HandlerFunc) *httputil.ReverseProxy {
	if host == "" {
		return nil
	}
	return &httputil.ReverseProxy{
		Rewrite: func(r *httputil.ProxyRequest) {
			port := r.In.Context().Value(portKey{}).(int)
			r.SetURL(&url.URL{Scheme: "http", Host: host + ":" + strconv.Itoa(port)})
			r.Out.Host = r.In.Host
			r.SetXForwarded()
			// Cloudflare terminates TLS; Traefik only ever sees http.
			r.Out.Header.Set("X-Forwarded-Proto", "https")
		},
		// Stream as it comes: video, server-sent events.
		FlushInterval: -1,
		ModifyResponse: func(resp *http.Response) error {
			if resp.StatusCode >= 502 && resp.StatusCode <= 504 {
				return errAppDown
			}
			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			down(w, r)
		},
	}
}

var errAppDown = &appDownError{}

type appDownError struct{}

func (*appDownError) Error() string { return "app answered 502-504" }
