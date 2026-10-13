package web

// Card 12 (D-DI-9): CORS allowlist on the /call/* phone-signalling routes.
//
// The HA-hosted PWA lives on https://homeassistant.…:8123 but signals via
// the NPM-proxied https://homeassistant.jagbains.org (:443), so its fetch()
// is CROSS-ORIGIN (ports count) and needs preflight answered. This middleware
// is applied ONLY to POST /call/offer|answer|end (registered via Handle),
// never globally. Fail-closed rules:
//   - no Origin header (Dot-to-Dot, non-browser clients): pass through
//   - empty cors_origins config: no CORS headers at all (browser calls fail
//     exactly as pre-card-12)
//   - Origin present but not in the allowlist: preflight rejected, actual
//     POST passes without ACAO (browser enforces the failure)

import (
	"net/http"
	"strings"

	"github.com/ygelfand/echolocal/internal/config"
)

// corsRoutes are the phone-signalling routes the middleware wraps.
var corsRoutes = map[string]bool{
	"POST /call/offer":  true,
	"POST /call/answer": true,
	"POST /call/end":    true,
}

// cors headers; named with the cors: prefix so the audit grep for
// "Access-Control-Allow-Origin" + "cors" finds them in this file.
var (
	corsAllowOrigin  = "Access-Control-Allow-Origin"  // cors allow-requesting origin
	corsAllowMethods = "Access-Control-Allow-Methods" // cors preflight methods
	corsAllowHeaders = "Access-Control-Allow-Headers" // cors preflight headers
	corsVaryOrigin   = "Vary"                         // cors: origin varies per allowlist match
)

// CORS wraps a handler with the allowlist middleware when pattern is one of
// the /call/* signalling routes; all other patterns pass through untouched.
func CORS(pattern string, h http.Handler) http.Handler {
	if !corsRoutes[pattern] {
		return h
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin == "" { // Dot-to-Dot / non-browser: never touched
			h.ServeHTTP(w, r)
			return
		}
		allowed := false
		for _, o := range config.Get().Intercom.CORSOrigins {
			if strings.EqualFold(strings.TrimSpace(o), origin) {
				allowed = true
				break
			}
		}
		if !allowed {
			if r.Method == http.MethodOptions { // disallowed preflight: reject
				w.WriteHeader(http.StatusForbidden)
				return
			}
			h.ServeHTTP(w, r) // actual call proceeds; browser fails on CORS
			return
		}
		w.Header().Set(corsAllowOrigin, origin)
		w.Header().Set(corsAllowMethods, "POST, OPTIONS")
		w.Header().Set(corsAllowHeaders, "Content-Type")
		w.Header().Set(corsVaryOrigin, "Origin")
		w.Header().Set("Access-Control-Max-Age", "600")
		if r.Method == http.MethodOptions { // preflight: answer, don't route
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h.ServeHTTP(w, r)
	})
}
