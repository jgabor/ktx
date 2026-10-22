// Package claudecode holds the static sources of the Claude Code plugins that
// `ktx init claude-code` generates. Paths in Files mirror the generated
// marketplace tree below its plugins directory.
package claudecode

import "embed"

// Files holds the plugin sources copied into the generated marketplace.
// Test files beside them are not embedded and are never generated.
//
//go:embed plugins/ktx/.claude-plugin/plugin.json
//go:embed plugins/ktx-stats/.claude-plugin/plugin.json
//go:embed plugins/ktx-stats/hooks/hooks.json
//go:embed plugins/ktx-stats/hooks/register.tsx
//go:embed plugins/ktx-stats/hooks/stats.ts
//go:embed plugins/ktx-stats/types/index.d.ts
var Files embed.FS
