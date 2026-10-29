package api

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestMergeOpencodeProviderAddsMissingModelsOnly(t *testing.T) {
	doc := map[string]any{
		"$schema": "https://opencode.ai/config.json",
		"provider": map[string]any{
			"myrouter": map[string]any{
				"options": map[string]any{"apiKey": "mr-sk-old", "timeout": 1000},
				"models": map[string]any{
					"existing": map[string]any{"name": "hand tuned", "variants": map[string]any{"high": map[string]any{}}},
				},
			},
			"other": map[string]any{"name": "keep me"},
		},
	}
	specs := map[string]opencodeModelSpec{
		"existing": {ID: "existing", Name: "existing (MyRouter)", Context: 128000},
		"new-one":  {ID: "new-one", Name: "new-one (MyRouter)", Context: 512000, Output: 384000, Tools: true, Reasoning: true},
	}

	added, removed, keyUpdated := mergeOpencodeProvider(doc, "http://127.0.0.1:20128/v1", "mr-sk-new", specs)

	if len(added) != 1 || added[0] != "new-one" {
		t.Fatalf("added = %v, want [new-one]", added)
	}
	if len(removed) != 0 {
		t.Fatalf("removed = %v, want []", removed)
	}
	if !keyUpdated {
		t.Fatal("api key update was not reported")
	}
	providers := doc["provider"].(map[string]any)
	if _, ok := providers["other"]; !ok {
		t.Fatal("unrelated providers must be preserved")
	}
	entry := providers["myrouter"].(map[string]any)
	opts := entry["options"].(map[string]any)
	if opts["apiKey"] != "mr-sk-new" {
		t.Fatalf("apiKey = %v", opts["apiKey"])
	}
	if opts["baseURL"] != "http://127.0.0.1:20128/v1" {
		t.Fatalf("baseURL = %v", opts["baseURL"])
	}
	if opts["timeout"] != 1000 {
		t.Fatal("existing timeout must not be overwritten")
	}
	models := entry["models"].(map[string]any)
	existing := models["existing"].(map[string]any)
	if existing["name"] != "hand tuned" {
		t.Fatal("an existing model entry must not be rewritten")
	}
	if _, ok := existing["variants"]; !ok {
		t.Fatal("existing variants must survive a sync")
	}
	addedModel := models["new-one"].(map[string]any)
	if addedModel["tool_call"] != true || addedModel["reasoning"] != true {
		t.Fatalf("capabilities not copied: %#v", addedModel)
	}
	limit := addedModel["limit"].(map[string]any)
	if limit["context"] != 512000 || limit["output"] != 384000 {
		t.Fatalf("limit = %#v", limit)
	}
}

func TestMergeOpencodeProviderLeavesKeyWhenNotProvided(t *testing.T) {
	doc := map[string]any{}
	_, _, keyUpdated := mergeOpencodeProvider(doc, "http://x/v1", "", map[string]opencodeModelSpec{})
	if keyUpdated {
		t.Fatal("key must only be reported as updated when one was supplied")
	}
	entry := doc["provider"].(map[string]any)["myrouter"].(map[string]any)
	opts := entry["options"].(map[string]any)
	if _, ok := opts["apiKey"]; ok {
		t.Fatal("apiKey must not be created out of thin air")
	}
	if opts["timeout"] == nil || opts["chunkTimeout"] == nil {
		t.Fatal("default timeouts should be filled in for a fresh provider block")
	}
}

func TestMergeOpencodeProviderPrunesStale(t *testing.T) {
	doc := map[string]any{"provider": map[string]any{"myrouter": map[string]any{
		"models": map[string]any{
			"keep":   map[string]any{"name": "hand tuned"},
			"stale1": map[string]any{"name": "old"},
			"stale2": map[string]any{"name": "old"},
		},
	}}}
	specs := map[string]opencodeModelSpec{"keep": {ID: "keep", Name: "keep (MyRouter)"}}
	added, removed, _ := mergeOpencodeProvider(doc, "http://x/v1", "", specs)
	if len(added) != 0 {
		t.Fatalf("added = %v, want []", added)
	}
	if len(removed) != 2 || removed[0] != "stale1" || removed[1] != "stale2" {
		t.Fatalf("removed = %v, want [stale1 stale2]", removed)
	}
	models := doc["provider"].(map[string]any)["myrouter"].(map[string]any)["models"].(map[string]any)
	if _, ok := models["stale1"]; ok {
		t.Fatal("stale1 not pruned")
	}
	if models["keep"].(map[string]any)["name"] != "hand tuned" {
		t.Fatal("keep entry must not be modified")
	}
}

func TestClampOpencodeLimits(t *testing.T) {
	ctx, out := clampOpencodeLimits(128000)
	if ctx != 128000 || out != 128000 {
		t.Fatalf("small window must keep real size: got %d/%d", ctx, out)
	}
	ctx, out = clampOpencodeLimits(1000000)
	if ctx != 1000000 || out != 384000 {
		t.Fatalf("1M window: got %d/%d", ctx, out)
	}
	ctx, out = clampOpencodeLimits(2097152)
	if ctx != 1000000 || out != 384000 {
		t.Fatalf("2M window must cap at 1M: got %d/%d", ctx, out)
	}
	ctx, out = clampOpencodeLimits(0)
	if ctx != 0 || out != 0 {
		t.Fatalf("zero window: got %d/%d", ctx, out)
	}
}

func TestStripJSONCommentsAndTrailingCommas(t *testing.T) {
	raw := []byte(`{
  // line comment
  "a": "http://not-a-comment", /* block */
  "b": [1, 2, 3,],
}`)
	var doc map[string]any
	if err := json.Unmarshal(stripJSONComments(raw), &doc); err != nil {
		t.Fatalf("stripJSONComments output is not valid JSON: %v", err)
	}
	if doc["a"] != "http://not-a-comment" {
		t.Fatalf("string content was damaged: %v", doc["a"])
	}
}

func TestWriteOpencodeDocKeepsBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode.json")
	if err := os.WriteFile(path, []byte("{\n  \"provider\": {}\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	backup, err := writeOpencodeDoc(path, map[string]any{"provider": map[string]any{"myrouter": map[string]any{}}})
	if err != nil {
		t.Fatalf("writeOpencodeDoc: %v", err)
	}
	if backup == "" {
		t.Fatal("expected a backup path for an existing config")
	}
	if _, err := os.Stat(backup); err != nil {
		t.Fatalf("backup missing: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("written config is not valid JSON: %v", err)
	}
}
