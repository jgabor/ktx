// Package claudecode holds the static sources of the Claude Code plugins that
// `ktx init claude-code` generates. Paths in Files mirror the generated
// marketplace tree below its plugins directory.
package claudecode

import "embed"

// Files holds the plugin sources copied into the generated marketplace.
// Test files beside them are not embedded and are never generated.
//
//go:embed plugins/ktx/.claude-plugin/plugin.json
var Files embed.FS
