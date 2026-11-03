package web

import (
	"context"
	"log"
	"net/http"
	"strings"
)

const uiCSP = "default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'; media-src 'self'; " +
	"connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'"

// csrfHeader must be on every state-changing request. Browsers won't send a
// custom header cross-origin without a CORS preflight, which this server
// never approves. Identity is ambient (it comes from the connection, not a
// cookie), so this is what stops other sites from acting as the visitor.
const csrfHeader = "X-Tailshare"

func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Content-Security-Policy", uiCSP)
		if s.opt.HTTPS {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}

		if !s.isHost(r.Host) {
			http.Error(w, "unknown host", http.StatusMisdirectedRequest)
			return
		}

		if strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Cache-Control", "no-store")
			switch r.Header.Get("Sec-Fetch-Site") {
			case "", "same-origin", "none":
			default:
				httpError(w, http.StatusForbidden, "cross-site request")
				return
			}
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				if r.Header.Get(csrfHeader) != "1" || !s.sameOrigin(r) {
					httpError(w, http.StatusForbidden, "missing or bad origin")
					return
				}
			}
		}

		u, err := s.opt.Resolver.Resolve(r.Context(), r.RemoteAddr)
		if err != nil {
			log.Printf("rejected %s: %v", r.RemoteAddr, err)
			http.Error(w, "tailshare only serves Tailscale users", http.StatusForbidden)
			return
		}
		if _, dup := s.seen.LoadOrStore(u.Node+"|"+u.Login, true); !dup {
			log.Printf("identity login=%s id=%d display=%q node=%s external=%v", u.Login, u.ID, u.Display, u.Node, u.External)
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, u)))
	})
}

func (s *Server) sameOrigin(r *http.Request) bool {
	o := r.Header.Get("Origin")
	if o == "" {
		return true
	}
	scheme := "http://"
	if s.opt.HTTPS {
		scheme = "https://"
	}
	host, ok := strings.CutPrefix(o, scheme)
	return ok && s.isHost(host)
}

func stripDefaultPort(h string) string {
	h = strings.TrimSuffix(h, ":443")
	return strings.TrimSuffix(h, ":80")
}
