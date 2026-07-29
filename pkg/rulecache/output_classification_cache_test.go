package rulecache

import (
	"reflect"
	"testing"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/model"
)

func TestOutputClassificationCacheRoundTrip(t *testing.T) {
	dataDir := t.TempDir()
	syncedAt := time.Date(2026, 7, 27, 8, 0, 0, 0, time.UTC)
	settings := model.OutputClassificationSettings{
		Revision:      7,
		CodeSuffixes:  []string{".html", ".ts"},
		CodeFilenames: []string{"Dockerfile"},
	}
	if err := SaveOutputClassificationCache(dataDir, settings, syncedAt); err != nil {
		t.Fatalf("SaveOutputClassificationCache: %v", err)
	}
	cache, err := LoadOutputClassificationCache(dataDir)
	if err != nil {
		t.Fatalf("LoadOutputClassificationCache: %v", err)
	}
	if cache.Revision != settings.Revision || !cache.SyncedAt.Equal(syncedAt) {
		t.Fatalf("cache metadata = %+v", cache)
	}
	if !reflect.DeepEqual(cache.CodeSuffixes, settings.CodeSuffixes) || !reflect.DeepEqual(cache.CodeFilenames, settings.CodeFilenames) {
		t.Fatalf("cache rules = %+v", cache)
	}
}
