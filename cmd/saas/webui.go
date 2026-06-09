package main

import (
	"io/fs"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	webui "quantlab/web"
)

// registerWebUI serves the embedded React SPA (web/dist) for every route the
// API router did not claim. In production (app_role=saas) this is the only way
// to reach the frontend; in dev the vite server proxies /api here instead, so
// embedded serving is a harmless fallback. It is a no-op when the embed has no
// index.html — i.e. a build that never ran `npm run build` (CI) — leaving the
// API as the only surface.
//
// Mounting is done via NoRoute (not a catch-all GET) so it never shadows a
// registered /api route: gin tries the real routes first and only falls
// through here on a miss.
func registerWebUI(r *gin.Engine) {
	dist, err := fs.Sub(webui.Dist, "dist")
	if err != nil {
		return
	}
	if _, err := fs.Stat(dist, "index.html"); err != nil {
		// No frontend build embedded — leave routing to the API only.
		return
	}
	fileServer := http.FileServer(http.FS(dist))

	r.NoRoute(func(c *gin.Context) {
		p := c.Request.URL.Path
		// The /api namespace owns its own 404s — never answer a missing API
		// route with index.html (that would turn a typo'd endpoint into a
		// 200 HTML page and mask the error).
		if strings.HasPrefix(p, "/api/") {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		// Serve the file if it exists (assets, favicon); otherwise hand back
		// index.html so react-router resolves the client-side route on a deep
		// link or a hard reload of e.g. /instances/abc.
		if rel := strings.TrimPrefix(p, "/"); rel != "" {
			if st, err := fs.Stat(dist, rel); err == nil && !st.IsDir() {
				fileServer.ServeHTTP(c.Writer, c.Request)
				return
			}
		}
		c.Request.URL.Path = "/"
		fileServer.ServeHTTP(c.Writer, c.Request)
	})
}
