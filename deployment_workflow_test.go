package main

import (
	"os"
	"strings"
	"testing"
)

func TestDeployWorkflowPublishesNginxPanelAssets(t *testing.T) {
	data, err := os.ReadFile(".github/workflows/deploy.yml")
	if err != nil {
		t.Fatalf("read deploy workflow: %v", err)
	}
	content := string(data)

	for _, want := range []string{
		`Upload panel assets`,
		`source: "manage.html,management.html,assets"`,
		`PANEL_SRC="/tmp/clirelay-panel-${{ github.sha }}"`,
		`PANEL_DIR="/home/web/html/relay-panel"`,
		`cp -a "$PANEL_SRC"/. "$PANEL_NEXT"/`,
		`mv "$PANEL_NEXT" "$PANEL_DIR"`,
	} {
		if !strings.Contains(content, want) {
			t.Fatalf("deploy workflow does not publish Nginx panel assets, missing %q", want)
		}
	}
}
