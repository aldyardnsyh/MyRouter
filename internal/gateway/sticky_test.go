package gateway

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/myrouter/myrouter/internal/model"
	"github.com/myrouter/myrouter/internal/provider"
	"github.com/myrouter/myrouter/internal/store"
)

func uoi(route *model.Route, sys string) *PreparedRequest {
	return &PreparedRequest{
		RequestID: "r", Route: route, Started: time.Now(),
		Input: &ChatInput{Model: "coding",
			Messages:    []provider.ChatMessage{provider.NewTextMessage("user", "q")},
			SystemExtra: sys},
	}
}

// Prefix-aware routing: same skills prefix sticks to same target (fail-open).
func TestStickyPrefixRoutesToSameTarget(t *testing.T) {
	g, db, _ := newTestGateway(t, "", "")
	route, _ := db.RouteByAlias("coding")
	prep := uoi(route, "## skills: deploy caddy gateway")
	out, err := g.execute(context.Background(), prep)
	if err != nil || out.Err != nil {
		t.Fatalf("exec1: %v %+v", err, out.Err)
	}
	first := out.Model.ModelID
	// Second request, same prefix → must hit same target.
	prep2 := uoi(route, "## skills: deploy caddy gateway")
	out2, err := g.execute(context.Background(), prep2)
	if err != nil || out2.Err != nil {
		t.Fatalf("exec2: %v %+v", err, out2.Err)
	}
	if out2.Model.ModelID != first {
		t.Errorf("sticky miss: got %s want %s", out2.Model.ModelID, first)
	}
	// Different prefix → no constraint (just must succeed, fail-open).
	prep3 := uoi(route, "## skills: totally different topic xyz")
	out3, err := g.execute(context.Background(), prep3)
	if err != nil || out3.Err != nil {
		t.Fatalf("exec3: %v %+v", err, out3.Err)
	}
}

// Fail-open: sticky target down → fallback chain still serves.
func TestStickyFailOpenWhenPinnedTargetDown(t *testing.T) {
	g, db, _ := newTestGateway(t, "500", "")
	route, _ := db.RouteByAlias("coding")
	if len(route.Targets) < 2 {
		t.Skip("need 2 targets")
	}
	// Pin prefix to the (failing) first target.
	g.stickyRecord(route.ID, "abc123", route.Targets[0].ModelID)
	prep := &PreparedRequest{
		RequestID: "r", Route: route, Started: time.Now(),
		Input: &ChatInput{Model: "coding",
			Messages:    []provider.ChatMessage{provider.NewTextMessage("user", "q")},
			SystemExtra: "x"},
	}
	// Force the same prefix hash by stubbing SystemExtra then overriding key:
	// record under the actual hash instead.
	h := prep.SkillPrefixHash()
	g.stickyRecord(route.ID, h, route.Targets[0].ModelID)
	out, err := g.execute(context.Background(), prep)
	if err != nil {
		t.Fatal(err)
	}
	if out.Err != nil {
		t.Fatalf("fail-open broken: %v", out.Err)
	}
	if !out.Fallback {
		t.Error("expected fallback to second target when pinned target fails")
	}
}

// Chat memory: captured preference is re-injected via QueryTerms.
func TestChatMemoryPreferenceReinjected(t *testing.T) {
	g, db, _ := newTestGateway(t, "", "")
	chat := &model.Skill{
		ID: store.NewID("skl"), Name: "chat-knowledge", Description: "m",
		Type: model.SkillChat, Enabled: true,
		Config:   `{"top_k":3,"max_chars":2000,"capture_keep":10}`,
		CreatedAt: store.Now(),
	}
	if err := db.CreateSkill(chat); err != nil {
		t.Fatal(err)
	}
	if err := db.SetSkillBindings(chat.ID, true, nil); err != nil {
		t.Fatal(err)
	}
	route, _ := db.RouteByAlias("coding")
	prep := &PreparedRequest{
		RequestID: "r1", Route: route,
		Input: &ChatInput{Model: "coding", Messages: []provider.ChatMessage{
			provider.NewTextMessage("user", "I prefer dark mode dashboards"),
		}},
	}
	prep.CaptureUser = lastUserText(prep.Input.Messages, 4000)
	prep.CaptureSkills = []model.Skill{*chat}
	g.captureSkills(prep, "Noted, I will use dark mode for your dashboards.")
	next := &PreparedRequest{
		RequestID: "r2", Route: route,
		Input: &ChatInput{Model: "coding", Messages: []provider.ChatMessage{
			provider.NewTextMessage("user", "build my dashboard please"),
		}},
	}
	g.applySkills(context.Background(), next)
	if !strings.Contains(strings.ToLower(next.Input.SystemExtra), "dark mode") {
		t.Errorf("preference not re-injected: %q", next.Input.SystemExtra)
	}
}
