// Package redlaunch embeds the operator guides shipped with Redlaunch.
package redlaunch

import "embed"

// Files contains only the public, versioned operator guides. Local configuration
// and environment files must never be included in the online help.
//
//go:embed README.md INSTALL.md RECIPES.md SSH_KEYS.md API_TOKENS.md
var Files embed.FS
