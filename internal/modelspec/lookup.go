package modelspec

import "strings"

// compatibleProviderType is MyRouter's provider type for generic
// OpenAI-compatible gateways (OpenRouter, Groq, vLLM, ...). Those providers are
// scoped by their display name instead of a fixed models.dev id.
const compatibleProviderType = "openai-compatible"

// providerAliases maps MyRouter provider types to models.dev provider ids.
var providerAliases = map[string]string{
	"openai":    "openai",
	"anthropic": "anthropic",
	"gemini":    "google",
	"deepseek":  "deepseek",
}

// Lookup resolves official metadata for a model. providerType is MyRouter's
// provider type ("openai", "anthropic", "gemini", "deepseek",
// "openai-compatible"), providerName is the configured provider name, and
// modelID is the upstream model id.
//
// Resolution order: the provider-scoped model id (exact, then normalized
// variants), then the global model-id index (exact, then normalized variants).
// It never performs network I/O and is deterministic for a given snapshot.
func (c *Catalog) Lookup(providerType, providerName, modelID string) (Spec, bool) {
	variants := modelIDVariants(modelID)
	if len(variants) == 0 {
		return Spec{}, false
	}
	scoped := scopedProviderIDs(providerType, providerName)

	c.mu.RLock()
	defer c.mu.RUnlock()
	if spec, ok := c.lookupScoped(scoped, variants[0]); ok {
		return spec, true
	}
	if spec, ok := c.global[variants[0]]; ok {
		return spec, true
	}
	for _, variant := range variants[1:] {
		if spec, ok := c.lookupScoped(scoped, variant); ok {
			return spec, true
		}
		if spec, ok := c.global[variant]; ok {
			return spec, true
		}
	}
	return Spec{}, false
}

// lookupScoped searches the candidate provider ids in order. The caller must
// hold at least a read lock.
func (c *Catalog) lookupScoped(providers []string, modelID string) (Spec, bool) {
	for _, pid := range providers {
		p, ok := c.providers[pid]
		if !ok {
			continue
		}
		if m, ok := p.Models[modelID]; ok {
			return m.Spec, true
		}
	}
	return Spec{}, false
}

// scopedProviderIDs returns the models.dev provider ids to try for a MyRouter
// provider. Typed providers map through the alias table; openai-compatible
// providers use their normalized display name ("OpenRouter" -> "openrouter").
func scopedProviderIDs(providerType, providerName string) []string {
	pt := normalizeKey(providerType)
	out := make([]string, 0, 2)
	seen := make(map[string]bool, 2)
	add := func(id string) {
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		out = append(out, id)
	}
	if id, ok := providerAliases[pt]; ok {
		add(id)
	}
	if pt == compatibleProviderType || pt == "" {
		add(normalizeProviderName(providerName))
	}
	return out
}

// normalizeKey lowercases and trims a models.dev provider or model id.
func normalizeKey(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// normalizeProviderID normalizes a models.dev provider id for storage.
func normalizeProviderID(s string) string {
	return normalizeKey(s)
}

// normalizeModelID normalizes a models.dev model id for storage.
func normalizeModelID(s string) string {
	return normalizeKey(s)
}

// normalizeProviderName turns a display name into a models.dev provider id:
// lowercase, runs of non-alphanumeric characters collapsed to a single dash,
// and no leading or trailing dashes ("OpenRouter" -> "openrouter").
func normalizeProviderName(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	lastDash := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// modelIDVariants expands a model id into the search candidates used by
// Lookup, most specific first:
//
//	exact lowercase
//	strip after ":" (Ollama-style ":tag")
//	strip after "@" (versioned variants)
//	strip vendor prefixes ("openai/gpt-4o" -> "gpt-4o")
//	strip trailing "-latest"
//	combinations of the above
func modelIDVariants(modelID string) []string {
	raw := normalizeKey(modelID)
	if raw == "" {
		return nil
	}
	out := make([]string, 0, 6)
	seen := make(map[string]bool, 6)
	add := func(s string) {
		s = strings.Trim(strings.TrimSpace(s), "/")
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}

	add(raw)

	cur := raw
	if i := strings.IndexByte(cur, ':'); i > 0 {
		cur = strings.TrimSpace(cur[:i])
		add(cur)
	}
	if i := strings.IndexByte(cur, '@'); i > 0 {
		cur = strings.TrimSpace(cur[:i])
		add(cur)
	}
	for {
		i := strings.IndexByte(cur, '/')
		if i <= 0 || i == len(cur)-1 {
			break
		}
		cur = cur[i+1:]
		add(cur)
	}
	add(strings.TrimSuffix(cur, "-latest"))
	return out
}
