package talentocli

import "embed"

// Content contains the reviewed gateway contract, canonical skill, and native wrappers.
//
//go:embed schemas/gateway.json coverage/manifest.json skills/talento all:plugins/talento all:plugins/claude-code
var Content embed.FS
