package chatbot

import (
	"os"
	"regexp"
	"testing"
)

// The app's icon registry and iconEmoji must hold the same ids. Skipped when the app repo is not
// checked out next to this one.
func TestIconEmojiCoversAppIcons(t *testing.T) {
	src, err := os.ReadFile("../../../../chubi-pocket-app/lib/ui/domain/icons/icon_registry.dart")
	if err != nil {
		t.Skip("app repo not found next to the backend")
	}
	ids := regexp.MustCompile(`(?m)^\s*'([a-z0-9_]+)':`).FindAllStringSubmatch(string(src), -1)
	if len(ids) == 0 {
		t.Fatal("no icon ids found in icon_registry.dart")
	}
	app := map[string]bool{}
	for _, m := range ids {
		app[m[1]] = true
		if _, ok := iconEmoji[m[1]]; !ok {
			t.Errorf("icon %q has no emoji", m[1])
		}
	}
	for id := range iconEmoji {
		if !app[id] {
			t.Errorf("emoji for %q, which the app no longer has", id)
		}
	}
}
