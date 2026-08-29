package commands

import (
	"testing"

	"github.com/talentohq/talento-cli/internal/app"
)

func TestUpgradeProgressOnlyUsesHumanOutput(t *testing.T) {
	tests := []struct {
		name    string
		options app.GlobalOptions
		want    bool
	}{
		{name: "human", want: true},
		{name: "json", options: app.GlobalOptions{JSON: true}},
		{name: "markdown", options: app.GlobalOptions{Markdown: true}},
		{name: "agent", options: app.GlobalOptions{Agent: true}},
		{name: "jq", options: app.GlobalOptions{JQ: ".installed"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			talento := &app.App{Global: &test.options}
			if got := upgradeProgressEnabled(talento); got != test.want {
				t.Fatalf("upgradeProgressEnabled() = %t, want %t", got, test.want)
			}
		})
	}
}
