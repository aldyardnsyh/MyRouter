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

	added, keyUpdated := mergeOpencodeProvider(doc, "http://127.0.0.1:20128/v1", "mr-sk-new", specs)

	if len(added) != 1 || added[0] != "new-one" {
		t.Fatalf("added = %v, want [new-one]", added)
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
	_, keyUpdated := mergeOpencodeProvider(doc, "http://x/v1", "", map[string]opencodeModelSpec{})
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
