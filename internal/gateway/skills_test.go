package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/myrouter/myrouter/internal/model"
	"github.com/myrouter/myrouter/internal/provider"
	"github.com/myrouter/myrouter/internal/store"
)

func TestApplySkillsInjectsContextAndCaptures(t *testing.T) {
	g, db, _ := newTestGateway(t, "", "")

	prompt := &model.Skill{
		ID: store.NewID("skl"), Name: "house-rules", Description: "rules", Type: model.SkillPrompt,
		Enabled: true, Config: `{"prompt":"Always answer in Indonesian."}`, CreatedAt: store.Now(),
	}
	chat := &model.Skill{
		ID: store.NewID("skl"), Name: "chat-knowledge", Description: "memory", Type: model.SkillChat,
		Enabled: true, Config: `{"top_k":3,"max_chars":2000,"capture_keep":10}`, CreatedAt: store.Now(),
	}
	for _, sk := range []*model.Skill{prompt, chat} {
		if err := db.CreateSkill(sk); err != nil {
			t.Fatalf("create skill: %v", err)
		}
		if err := db.SetSkillBindings(sk.ID, true, nil); err != nil {
			t.Fatalf("bind skill: %v", err)
		}
	}

	route, err := db.RouteByAlias("coding")
	if err != nil {
		t.Fatal(err)
	}
	prep := &PreparedRequest{
		RequestID: "req-skills",
		Input: &ChatInput{Model: "coding", Messages: []provider.ChatMessage{
			provider.NewTextMessage("system", "client system"),
			provider.NewTextMessage("user", "How do I deploy this gateway?"),
		}},
		Route: route, EstTokens: 10,
	}
	baseline := prep.EstTokens

	rt := g.applySkills(context.Background(), prep)
	if prep.Input.SystemExtra == "" {
		t.Fatal("expected skill context to be injected")
	}
	if !strings.Contains(prep.Input.SystemExtra, "Always answer in Indonesian.") {
		t.Errorf("prompt skill missing: %q", prep.Input.SystemExtra)
	}
	if !strings.Contains(prep.Input.SystemExtra, "## MyRouter skills") {
		t.Errorf("missing skills header: %q", prep.Input.SystemExtra)
	}
	if prep.EstTokens <= baseline {
		t.Errorf("token estimate should grow after injection: %d -> %d", baseline, prep.EstTokens)
	}
	if len(rt.names) != 1 || rt.names[0] != "house-rules" {
		t.Errorf("unexpected applied names: %v", rt.names)
	}
	if prep.SkillTrace == "" {
		t.Error("expected a skill trace")
	}
	trace := prep.traceWith(nil)
	if len(trace) != 1 || trace[0].Category != "skills" {
		t.Errorf("traceWith did not prepend the skills step: %+v", trace)
	}

	// Capturing the answer stores it in the chat skill and a later request with
	// a similar prompt retrieves it.
	prep.CaptureUser = lastUserText(prep.Input.Messages, 4000)
	g.captureSkills(prep, "The gateway runs as a single binary behind Caddy.")
	items, _ := db.ListSkillItems(chat.ID, 10)
	if len(items) != 1 {
		t.Fatalf("expected one captured exchange, got %d", len(items))
	}
	next := &PreparedRequest{
		RequestID: "req-2",
		Input: &ChatInput{Model: "coding", Messages: []provider.ChatMessage{
			provider.NewTextMessage("user", "How do I deploy the gateway with Caddy?"),
		}},
		Route: route, EstTokens: 10,
	}
	g.applySkills(context.Background(), next)
	if !strings.Contains(next.Input.SystemExtra, "Caddy") {
		t.Errorf("expected captured chat knowledge to be retrieved, got %q", next.Input.SystemExtra)
	}
}

func TestApplySkillsWithoutBindingsIsNoop(t *testing.T) {
	g, db, _ := newTestGateway(t, "", "")
	sk := &model.Skill{
		ID: store.NewID("skl"), Name: "unbound", Description: "x", Type: model.SkillPrompt,
		Enabled: true, Config: `{"prompt":"should not appear"}`, CreatedAt: store.Now(),
	}
	if err := db.CreateSkill(sk); err != nil {
		t.Fatal(err)
	}
	route, _ := db.RouteByAlias("coding")
	prep := &PreparedRequest{
		Input: &ChatInput{Messages: []provider.ChatMessage{provider.NewTextMessage("user", "hi")}},
		Route: route,
	}
	g.applySkills(context.Background(), prep)
	if prep.Input.SystemExtra != "" {
		t.Fatalf("unbound skill must not be injected: %q", prep.Input.SystemExtra)
	}
}

func TestFindAndRewriteImages(t *testing.T) {
	content, _ := json.Marshal([]map[string]any{
		{"type": "text", "text": "what is in this screenshot?"},
		{"type": "image_url", "image_url": map[string]any{"url": "https://example.com/a.png"}},
		{"type": "input_image", "image_url": "https://example.com/b.png"},
	})
	msgs := []provider.ChatMessage{{Role: "user", Content: content}}
	images := findImages(msgs)
	if len(images) != 2 {
		t.Fatalf("expected 2 images, got %d", len(images))
	}
	if images[0].url != "https://example.com/a.png" || images[1].url != "https://example.com/b.png" {
		t.Errorf("unexpected image urls: %+v", images)
	}
	if !rewriteContentPart(msgs, images[0], "[Image described: a dashboard]") {
		t.Fatal("rewrite failed")
	}
	rewritten := string(msgs[0].Content)
	if strings.Contains(rewritten, "a.png") {
		t.Error("image url should be gone")
	}
	if !strings.Contains(rewritten, "a dashboard") {
		t.Error("description should replace the image")
	}
}

func TestLastUserText(t *testing.T) {
	msgs := []provider.ChatMessage{
		provider.NewTextMessage("user", "first"),
		provider.NewTextMessage("assistant", "answer"),
		provider.NewTextMessage("user", "second question"),
	}
	if got := lastUserText(msgs, 100); got != "second question" {
		t.Errorf("got %q", got)
	}
	if got := lastUserText(nil, 100); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestHasCapability(t *testing.T) {
	if !hasCapability("chat,vision,tools", "vision") {
		t.Error("expected vision capability")
	}
	if hasCapability("chat,tools", "vision") {
		t.Error("unexpected vision capability")
	}
}

// TestVisionBridgeRewritesImages covers the vision skill end to end: a
// text-only primary model triggers a call to a configured vision route and the
// image part is replaced with the description before the chat is executed.
func TestVisionBridgeRewritesImages(t *testing.T) {
	g, db, _ := newTestGateway(t, "", "")

	var visionCalls int
	visionSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		visionCalls++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"1","model":"vision-model","choices":[{"message":{"role":"assistant","content":"a cat sitting on a keyboard"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":5,"total_tokens":10}}`)
	}))
	defer visionSrv.Close()

	now := store.Now()
	p3 := &model.Provider{ID: "p3", Name: "Vision", Type: "openai-compatible", BaseURL: visionSrv.URL, Enabled: true, CreatedAt: now}
	if err := db.CreateProvider(p3); err != nil {
		t.Fatal(err)
	}
	ct, n, _ := g.box.Encrypt("k3")
	_ = db.SaveCredential(p3.ID, ct, n, 1, "k3")
	vm, err := db.UpsertModel(&model.Model{ProviderID: p3.ID, ModelID: "vision-model", DisplayName: "V", Capabilities: "chat,vision", Available: true})
	if err != nil {
		t.Fatal(err)
	}
	vr := &model.Route{Alias: "vision-route", Enabled: true, Strategy: "priority", Retry: 0, TimeoutMS: 5000}
	vr.Targets = []model.RouteTarget{{ModelID: vm, Role: "primary"}}
	if err := db.SaveRoute(vr); err != nil {
		t.Fatal(err)
	}

	skill := &model.Skill{
		ID: store.NewID("skl"), Name: "vision", Description: "vision bridge", Type: model.SkillVision,
		Enabled: true, Config: `{"vision_route":"vision-route","max_images":2,"max_chars":500}`, CreatedAt: now,
	}
	if err := db.CreateSkill(skill); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSkillBindings(skill.ID, true, nil); err != nil {
		t.Fatal(err)
	}

	content, _ := json.Marshal([]map[string]any{
		{"type": "text", "text": "what is in this image?"},
		{"type": "image_url", "image_url": map[string]any{"url": "https://example.com/cat.png"}},
	})
	route, _ := db.RouteByAlias("coding")
	prep := &PreparedRequest{
		Input: &ChatInput{Model: "coding", Messages: []provider.ChatMessage{{Role: "user", Content: content}}},
		Route: route,
	}
	g.applySkills(context.Background(), prep)

	if visionCalls != 1 {
		t.Fatalf("expected exactly one vision call, got %d", visionCalls)
	}
	rewritten := string(prep.Input.Messages[0].Content)
	if strings.Contains(rewritten, "cat.png") {
		t.Error("image part was not replaced")
	}
	if !strings.Contains(rewritten, "a cat sitting on a keyboard") {
		t.Errorf("vision description missing: %q", rewritten)
	}
	if !strings.Contains(prep.SkillTrace, "vision: 1 image(s)") {
		t.Errorf("trace missing vision detail: %q", prep.SkillTrace)
	}

	// The description is cached: a second identical request does not call the
	// vision model again.
	prep2 := &PreparedRequest{
		Input: &ChatInput{Model: "coding", Messages: []provider.ChatMessage{{Role: "user", Content: content}}},
		Route: route,
	}
	g.applySkills(context.Background(), prep2)
	if visionCalls != 1 {
		t.Fatalf("vision cache miss: got %d calls", visionCalls)
	}
}
