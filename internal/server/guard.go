package server

import (
	"mime"
	"net"
	"net/http"
)

// guard stops other websites from using SchemaLens through your browser.
//
// Any page you visit can send requests to http://localhost:8080. Two things
// would make that dangerous here, since /api/connect makes SchemaLens open
// database connections:
//
//   - CSRF: a page POSTs a connect request. Browsers only let a cross-site
//     page send a POST without asking first if it's a "simple" request (a
//     form encoding). So we accept only application/json, which needs a
//     CORS preflight we never answer, and we also reject a foreign Origin.
//   - DNS rebinding: a page on evil.example re-points its own name at
//     127.0.0.1, which makes its requests same-origin. The Host header still
//     says evil.example, so when we're listening on localhost, we refuse any
//     Host that isn't localhost.
func guard(next http.Handler, localOnly bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if localOnly && !isLocalHost(r.Host) {
			http.Error(w, "SchemaLens only answers to localhost", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			if origin := r.Header.Get("Origin"); origin != "" && origin != "http://"+r.Host {
				http.Error(w, "cross-site request refused", http.StatusForbidden)
				return
			}
			if r.Method == http.MethodPost {
				ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
				if ct != "application/json" {
					http.Error(w, "requests must be application/json", http.StatusUnsupportedMediaType)
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

func isLocalHost(hostport string) bool {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		host = hostport // no port given
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
