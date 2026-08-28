package quota

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTiersFile(t *testing.T, contents string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "tiers.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write tiers fixture: %v", err)
	}
	return path
}

func TestLoadTiers_Valid(t *testing.T) {
	path := writeTiersFile(t, `
tiers:
  FREE:
    max_channels: 1
    max_channel_members: 3
    messages_per_minute: 20
  PRO:
    max_channels: 50
    max_channel_members: 500
    messages_per_minute: 600
`)

	tiers, err := LoadTiers(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	free, ok := tiers[TierFree]
	if !ok {
		t.Fatalf("expected %q tier to be present", TierFree)
	}
	if free.MaxChannels != 1 || free.MaxChannelMembers != 3 || free.MessagesPerMinute != 20 {
		t.Fatalf("FREE tier limits parsed incorrectly: %+v", free)
	}
	if tiers[TierPro].MaxChannels != 50 {
		t.Fatalf("PRO tier limits parsed incorrectly: %+v", tiers[TierPro])
	}
}

func TestLoadTiers_MissingFile(t *testing.T) {
	if _, err := LoadTiers(filepath.Join(t.TempDir(), "does-not-exist.yaml")); err == nil {
		t.Fatalf("expected an error for a missing tiers file")
	}
}

func TestLoadTiers_MalformedYAML(t *testing.T) {
	path := writeTiersFile(t, "tiers: [this is not a map")
	if _, err := LoadTiers(path); err == nil {
		t.Fatalf("expected an error for malformed YAML")
	}
}

func TestLoadTiers_EmptyTiersRejected(t *testing.T) {
	path := writeTiersFile(t, "tiers: {}\n")
	if _, err := LoadTiers(path); err == nil {
		t.Fatalf("expected an error when the tiers file defines no tiers")
	}
}
