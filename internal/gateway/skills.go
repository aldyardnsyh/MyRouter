package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/myrouter/myrouter/internal/logx"
	"github.com/myrouter/myrouter/internal/model"
	"github.com/myrouter/myrouter/internal/provider"
	"github.com/myrouter/myrouter/internal/skills"
	"github.com/myrouter/myrouter/internal/store"
)

// skillRuntime carries the per-request skill decisions from the injection step
// to the post-response capture step.
type skillRuntime struct {
	trace    string
	names    []string
	userText string
	captures []model.Skill
}

// traceWith prepends one trace step describing the skills applied to a request.
func (p *PreparedRequest) traceWith(trace []model.TraceStep) []model.TraceStep {
	if p.SkillTrace == "" {
		return trace
	}
	step := model.TraceStep{
		Attempt: 0, Outcome: "skills", Category: "skills", Message: p.SkillTrace,
	}
	out := make([]model.TraceStep, 0, len(trace)+1)
	out = append(out, step)
	out = append(out, trace...)
	return out
}

// applySkills runs the skills pipeline for one request:
//
//  1. resolve the enabled skills bound to this route (global + route bindings),
//  2. vision bridge: describe image parts with a vision-capable model when the
//     primary target cannot see images,
//  3. assemble the prompt/retrieval context and attach it as system extra,
//  4. remember which chat skills should capture the completed exchange.
//
// Skills are fail-open: any error only drops the skill context, it never blocks
// the user request.
func (g *Gateway) applySkills(ctx context.Context, prep *PreparedRequest) *skillRuntime {
	rt := &skillRuntime{userText: lastUserText(prep.Input.Messages, 4000)}
	effective, err := g.db.EffectiveSkills(prep.Route.Alias)
	if err != nil {
		g.log.Debug("skills: resolve failed", logx.F("error", err.Error()))
		return rt
	}
	if len(effective) == 0 {
		return rt
	}

	// On-request sync: retrieval skills with sync_paths refresh from disk as
	// part of the request flow (first sync blocks once, later ones refresh in
	// the background). Fail-open: never blocks or fails the user request.
	skills.EnsureFreshForRequest(g.db, g.log, effective)
	// Re-resolve after a possible first sync so fresh items are visible.
	if refreshed, err := g.db.EffectiveSkills(prep.Route.Alias); err == nil && len(refreshed) > 0 {
		effective = refreshed
	}

	var parts []string
	var names []string
	// Vision bridge first: it rewrites the messages (images -> descriptions)
	// before the text context is assembled.
	for _, s := range effective {
		if s.Type != model.SkillVision {
			continue
		}
		cfg := skills.ParseConfig(s.Config)
		n, err := g.bridgeVision(ctx, prep, cfg)
		if err != nil {
			g.log.Debug("skills: vision bridge failed", logx.F("error", err.Error()))
		}
		if n > 0 {
			parts = append(parts, fmt.Sprintf("%s: %d image(s)", s.Name, n))
			names = append(names, s.Name)
		}
	}

	res, err := skills.BuildFor(g.db, effective, rt.userText)
	if err != nil {
		g.log.Debug("skills: build failed", logx.F("error", err.Error()))
	} else if strings.TrimSpace(res.Context) != "" {
		prep.Input.SystemExtra = res.Context
		for _, a := range res.Applied {
			parts = append(parts, fmt.Sprintf("%s: %s", a.Skill, a.Detail))
			names = append(names, a.Skill)
		}
	}

	for _, s := range effective {
		if s.Type == model.SkillChat {
			rt.captures = append(rt.captures, s)
		}
	}
	rt.names = dedupeStrings(names)
	rt.trace = strings.Join(parts, " | ")
	prep.SkillTrace = rt.trace
	prep.CaptureSkills = rt.captures
	prep.CaptureUser = rt.userText
	// Re-estimate tokens after injection so Guard sees the real prompt size.
	prep.EstTokens = EstimateTokens(prep.Input.Messages) + 256 + len(prep.Input.SystemExtra)/4
	return rt
}

// captureSkills stores completed exchanges for every chat skill of the request.
// It runs after the response is assembled and never delays the client.
func (g *Gateway) captureSkills(prep *PreparedRequest, answer string) {
	if prep == nil || len(prep.CaptureSkills) == 0 || strings.TrimSpace(answer) == "" {
		return
	}
	for _, s := range prep.CaptureSkills {
		if err := skills.Capture(g.db, s, prep.Route.Alias, prep.CaptureUser, answer); err != nil {
			g.log.Debug("skills: capture failed", logx.F("skill", s.Name), logx.F("error", err.Error()))
		}
	}
}

// dedupeStrings keeps the first occurrence order.
func dedupeStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// lastUserText returns the newest user message body, capped at max runes.
func lastUserText(messages []provider.ChatMessage, max int) string {
	for i := len(messages) - 1; i >= 0; i-- {
		if messages[i].Role != "user" {
			continue
		}
		text := strings.TrimSpace(messages[i].Text())
		if text != "" {
			return skills.Truncate(text, max)
		}
	}
	return ""
}

// visionPart is one image occurrence inside a message content array.
type visionPart struct {
	msgIndex  int
	partIndex int
	url       string
}

// bridgeVision rewrites image content parts into text descriptions. It returns
// how many images were replaced. Images are left untouched when the primary
// target model already supports vision.
func (g *Gateway) bridgeVision(ctx context.Context, prep *PreparedRequest, cfg skills.Config) (int, error) {
	images := findImages(prep.Input.Messages)
	if len(images) == 0 {
		return 0, nil
	}
	if g.primarySupportsVision(prep.Route) && !cfg.Always {
		return 0, nil
	}
	mdl, prov, ok := g.resolveVisionTarget(prep.Route, cfg.VisionRoute)
	if !ok {
		return 0, fmt.Errorf("no vision-capable model available; configure the vision skill's vision_route")
	}
	if len(images) > cfg.MaxImages {
		images = images[:cfg.MaxImages]
	}
	replaced := 0
	for _, img := range images {
		desc, err := g.describeImage(ctx, mdl, prov, cfg.VisionPrompt, cfg.MaxChars, img.url)
		if err != nil {
			g.log.Debug("skills: describe image failed", logx.F("error", err.Error()))
		}
		text := "[Image could not be analyzed by the gateway]"
		if err == nil && strings.TrimSpace(desc) != "" {
			text = "[Image described by MyRouter vision skill: " + desc + "]"
		}
		if rewriteContentPart(prep.Input.Messages, img, text) {
			replaced++
		}
	}
	return replaced, nil
}

// primarySupportsVision reports whether the route's primary model can see images.
func (g *Gateway) primarySupportsVision(route *model.Route) bool {
	if route == nil {
		return false
	}
	t, ok := route.Primary()
	if !ok {
		return false
	}
	mdl, err := g.db.ModelByID(t.ModelID)
	if err != nil {
		return false
	}
	return hasCapability(mdl.Capabilities, "vision")
}

// resolveVisionTarget picks the model that describes images. An explicit
// vision_route alias wins; "auto" prefers a cheap model that advertises vision.
func (g *Gateway) resolveVisionTarget(route *model.Route, alias string) (*model.Model, *model.Provider, bool) {
	alias = strings.TrimSpace(alias)
	if alias != "" && !strings.EqualFold(alias, "auto") {
		rt, err := g.db.RouteByAlias(alias)
		if err != nil {
			return nil, nil, false
		}
		t, ok := rt.Primary()
		if !ok {
			return nil, nil, false
		}
		mdl, err := g.db.ModelByID(t.ModelID)
		if err != nil {
			return nil, nil, false
		}
		prov, err := g.db.ProviderByID(mdl.ProviderID)
		if err != nil || !prov.Enabled || !mdl.Available {
			return nil, nil, false
		}
		return mdl, prov, true
	}
	// auto: never route through the same request route (that would be the
	// text-only model we are trying to help).
	var best *model.Model
	bestPrice := 0.0
	models, err := g.db.ListModels()
	if err != nil {
		return nil, nil, false
	}
	for _, m := range models {
		if !m.Available || !hasCapability(m.Capabilities, "vision") {
			continue
		}
		if route != nil && routeContainsModel(route, m.ID) {
			continue
		}
		price := 0.0
		if m.PriceIn != nil {
			price = *m.PriceIn
		}
		if best == nil || price < bestPrice {
			best = &m
			bestPrice = price
		}
	}
	if best == nil {
		return nil, nil, false
	}
	prov, err := g.db.ProviderByID(best.ProviderID)
	if err != nil || !prov.Enabled {
		return nil, nil, false
	}
	return best, prov, true
}

func routeContainsModel(route *model.Route, modelID string) bool {
	for _, t := range route.Targets {
		if t.ModelID == modelID {
			return true
		}
	}
	return false
}

// describeImage calls the vision model with one image and caches the result.
func (g *Gateway) describeImage(ctx context.Context, mdl *model.Model, prov *model.Provider, prompt string, maxChars int, url string) (string, error) {
	if prompt == "" {
		prompt = "Describe this image precisely for a text-only model. Include visible text verbatim and any details needed to answer a follow-up question."
	}
	sum := sha256.Sum256([]byte(mdl.ID + "|" + prompt + "|" + url))
	key := hex.EncodeToString(sum[:])
	if desc, ok := g.db.VisionCacheGet(key); ok {
		return desc, nil
	}
	creds := provider.Credentials{APIKey: g.decryptCredentialOrEmpty(prov.ID), BaseURL: prov.BaseURL}
	content, _ := json.Marshal([]map[string]any{
		{"type": "text", "text": prompt},
		{"type": "image_url", "image_url": map[string]any{"url": url}},
	})
	req := provider.ChatRequest{
		Model:     mdl.ModelID,
		Messages:  []provider.ChatMessage{{Role: "user", Content: content}},
		MaxTokens: 512,
	}
	adapter := g.registry.Get(prov.Type)
	resp, err := adapter.Chat(ctx, creds, mdl.ModelID, req)
	if err != nil {
		return "", err
	}
	desc := strings.TrimSpace(resp.Content)
	if maxChars > 0 {
		desc = skills.Truncate(desc, maxChars)
	}
	if desc == "" {
		return "", fmt.Errorf("vision model returned an empty description")
	}
	_ = g.db.VisionCachePut(key, desc)
	return desc, nil
}

func (g *Gateway) decryptCredentialOrEmpty(providerID string) string {
	key, err := g.decryptCredential(providerID)
	if err != nil {
		return ""
	}
	return key
}

// findImages lists image parts in message content arrays.
func findImages(messages []provider.ChatMessage) []visionPart {
	var out []visionPart
	for mi, m := range messages {
		raw := strings.TrimSpace(string(m.Content))
		if raw == "" || raw[0] != '[' {
			continue
		}
		var parts []map[string]any
		if json.Unmarshal(m.Content, &parts) != nil {
			continue
		}
		for pi, p := range parts {
			typ, _ := p["type"].(string)
			switch typ {
			case "image_url", "input_image", "image":
			default:
				continue
			}
			if url := imageURL(p); url != "" {
				out = append(out, visionPart{msgIndex: mi, partIndex: pi, url: url})
			}
		}
	}
	return out
}

// imageURL extracts a URL or data payload from an image part in either the
// Chat Completions (object) or Responses (string) shape.
func imageURL(part map[string]any) string {
	switch v := part["image_url"].(type) {
	case string:
		return v
	case map[string]any:
		if s, ok := v["url"].(string); ok {
			return s
		}
	}
	if v, ok := part["url"].(string); ok {
		return v
	}
	if src, ok := part["source"].(map[string]any); ok {
		if data, ok := src["data"].(string); ok {
			if typ, _ := src["media_type"].(string); typ != "" {
				return "data:" + typ + ";base64," + data
			}
			return data
		}
		if u, ok := src["url"].(string); ok {
			return u
		}
	}
	return ""
}

// rewriteContentPart replaces one image part with a text part in place.
func rewriteContentPart(messages []provider.ChatMessage, img visionPart, text string) bool {
	if img.msgIndex < 0 || img.msgIndex >= len(messages) {
		return false
	}
	var parts []map[string]any
	if json.Unmarshal(messages[img.msgIndex].Content, &parts) != nil {
		return false
	}
	if img.partIndex < 0 || img.partIndex >= len(parts) {
		return false
	}
	parts[img.partIndex] = map[string]any{"type": "text", "text": text}
	buf, err := json.Marshal(parts)
	if err != nil {
		return false
	}
	messages[img.msgIndex].Content = buf
	return true
}

// hasCapability checks a comma-separated capability column.
func hasCapability(csv, want string) bool {
	for _, c := range store.SplitCSV(csv) {
		if strings.EqualFold(strings.TrimSpace(c), want) {
			return true
		}
	}
	return false
}
