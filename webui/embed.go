// Package webui embeds the pre-built web UI shell served under /ui.
//
// The bundle in dist/ is dependency-free (no Node build step): it is a static
// HTML/CSS/JS shell that talks to the /ui/manifest, /ui/chat and /ui/events
// bridge endpoints. Phase 6 replaces/augments it with a CopilotKit generative-UI
// front-end; keeping the bundle committed means `go build` never needs Node.
package webui

import "embed"

// Dist holds the static web UI bundle (index.html, app.js, style.css, ...).
//
//go:embed dist
var Dist embed.FS
