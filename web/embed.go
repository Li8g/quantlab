// Package webui embeds the built React SPA (web/dist) so the saas binary can
// serve the frontend same-origin in production — no nginx or vite needed
// (docs/frontend-promote-retire-v1.md §2 same-origin model). The dist tree is
// produced by `npm run build` in web/. A fresh checkout that has not built the
// frontend embeds only the .gitkeep placeholder; the server detects the
// missing index.html and skips UI routing (the API still serves normally).
package webui

import "embed"

// Dist holds the built SPA. all: includes the .gitkeep dotfile so the embed
// pattern always matches at least one file and the package compiles even
// before `npm run build` has run (e.g. in CI).
//
//go:embed all:dist
var Dist embed.FS
