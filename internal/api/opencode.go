package api

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/myrouter/myrouter/internal/logx"
	"github.com/myrouter/myrouter/internal/store"
)

// opencodeProviderID is the provider key MyRouter manages inside the OpenCode
// configuration. Everything else in that file is left untouched.
const opencodeProviderID = "myrouter"

// opencodeModelSpec is the subset of a route MyRouter needs to describe it to
// OpenCode.
type opencodeModelSpec struct {
	ID        string
	Name      string
	Context   int
	Output    int
	Vision    bool
	Tools     bool
	Reasoning bool
}

// opencodeStatus describes the gap between the gateway routes and the models
// OpenCode knows about.
type opencodeStatus struct {
	Path             string   `json:"path"`
	Exists           bool     `json:"exists"`
	Writable         bool     `json:"writable"`
	ProviderID       string   `json:"provider_id"`
	ProviderFound    bool     `json:"provider_found"`
	BaseURL          string   `json:"base_url"`
	ConfigModels     []string `json:"config_models"`
	RouteModels      []string `json:"route_models"`
	Missing          []string `json:"missing"`
	Stale            []string `json:"stale"`
	APIKeyConfigured bool     `json:"api_key_configured"`
	ListenAddr       string   `json:"listen_addr"`
	DataDir          string   `json:"data_dir"`
}

// opencodeConfigPath resolves the OpenCode config file. OPENCODE_CONFIG wins,
// then XDG_CONFIG_HOME, then ~/.config. When both opencode.json and
// opencode.jsonc exist the JSON file is preferred because it can be rewritten
// without destroying comments.
func opencodeConfigPath() string {
	if v := strings.TrimSpace(os.Getenv("OPENCODE_CONFIG")); v != "" {
		return v
	}
	base := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME"))
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".config")
	}
	dir := filepath.Join(base, "opencode")
	jsonPath := filepath.Join(dir, "opencode.json")
	if _, err := os.Stat(jsonPath); err == nil {
		return jsonPath
	}
	jsoncPath := filepath.Join(dir, "opencode.jsonc")
	if _, err := os.Stat(jsoncPath); err == nil {
		return jsoncPath
	}
	return jsonPath
}

// opencodeWritable reports whether MyRouter may rewrite the config in place.
// A .jsonc file is read-only for us: reformatting it would silently drop the
// operator's comments.
func opencodeWritable(path string) bool {
	return strings.EqualFold(filepath.Ext(path), ".json")
}

// stripJSONComments removes // and /* */ comments and trailing commas outside
// of string literals, which is enough to read an opencode.jsonc file.
func stripJSONComments(raw []byte) []byte {
	out := make([]byte, 0, len(raw))
	inString := false
	escaped := false
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if inString {
			out = append(out, c)
			if escaped {
				escaped = false
				continue
			}
			switch c {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
			out = append(out, c)
			continue
		}
		if c == '/' && i+1 < len(raw) {
			switch raw[i+1] {
			case '/':
				for i < len(raw) && raw[i] != '\n' {
					i++
				}
				out = append(out, '\n')
				continue
			case '*':
				i += 2
				for i+1 < len(raw) && !(raw[i] == '*' && raw[i+1] == '/') {
					i++
				}
				i++
				continue
			}
		}
		out = append(out, c)
	}
	return dropTrailingCommas(out)
}

// dropTrailingCommas removes a comma that is followed only by whitespace and a
// closing bracket.
func dropTrailingCommas(raw []byte) []byte {
	out := make([]byte, 0, len(raw))
	inString := false
	escaped := false
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if inString {
			out = append(out, c)
			if escaped {
				escaped = false
				continue
			}
			switch c {
			case '\\':
				escaped = true
			case '"':
				inString = false
			}
			continue
		}
		if c == '"' {
			inString = true
			out = append(out, c)
			continue
		}
		if c == ',' {
			j := i + 1
			for j < len(raw) && (raw[j] == ' ' || raw[j] == '\t' || raw[j] == '\n' || raw[j] == '\r') {
				j++
			}
			if j < len(raw) && (raw[j] == '}' || raw[j] == ']') {
				continue
			}
		}
		out = append(out, c)
	}
	return out
}

// readOpencodeDoc loads the config as a generic document so unknown keys are
// preserved on write.
func readOpencodeDoc(path string) (map[string]any, bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{}, false, nil
		}
		return nil, false, err
	}
	var doc map[string]any
	if err := json.Unmarshal(stripJSONComments(raw), &doc); err != nil {
		return nil, true, fmt.Errorf("opencode config is not valid JSON: %w", err)
	}
	if doc == nil {
		doc = map[string]any{}
	}
	return doc, true, nil
}

// opencodeProviderEntry returns the provider object, creating it when missing.
func opencodeProviderEntry(doc map[string]any) map[string]any {
	providers, ok := doc["provider"].(map[string]any)
	if !ok {
		providers = map[string]any{}
		doc["provider"] = providers
	}
	entry, ok := providers[opencodeProviderID].(map[string]any)
	if !ok {
		entry = map[string]any{}
		providers[opencodeProviderID] = entry
	}
	return entry
}

func opencodeOptions(entry map[string]any) map[string]any {
	opts, ok := entry["options"].(map[string]any)
	if !ok {
		opts = map[string]any{}
		entry["options"] = opts
	}
	return opts
}

func opencodeModels(entry map[string]any) map[string]any {
	models, ok := entry["models"].(map[string]any)
	if !ok {
		models = map[string]any{}
		entry["models"] = models
	}
	return models
}

// opencodeMaxContext caps the context advertised to OpenCode at 1M tokens.
// OpenCode uses limit.context for compaction/auto-compact decisions: claiming
// more than the upstream model supports makes OpenCode delay compaction and
// the request dies mid-stream with "out of context". So small models keep
// their real window, large models are capped at 1M.
const opencodeMaxContext = 1000000

// opencodeMaxOutput caps advertised output (OpenCode limit.output).
const opencodeMaxOutput = 384000

// clampOpencodeLimits returns (context, output) for a model window.
func clampOpencodeLimits(window int) (int, int) {
	if window <= 0 {
		return 0, 0
	}
	ctx := window
	if ctx > opencodeMaxContext {
		ctx = opencodeMaxContext
	}
	out := opencodeMaxOutput
	if ctx < out {
		out = ctx
	}
	return ctx, out
}

// opencodeModelEntry builds one model block in the shape OpenCode expects.
func opencodeModelEntry(spec opencodeModelSpec) map[string]any {
	entry := map[string]any{
		"name":       spec.Name,
		"reasoning":  spec.Reasoning,
		"attachment": spec.Vision,
		"tool_call":  spec.Tools,
	}
	if spec.Context > 0 {
		limit := map[string]any{"context": spec.Context}
		if spec.Output > 0 {
			limit["output"] = spec.Output
		}
		entry["limit"] = limit
	}
	return entry
}

// mergeOpencodeProvider writes base URL, optional API key, adds missing routes,
// and prunes models no longer in specs. Existing entries still in specs are
// never modified, so hand-tuned variants and names survive a sync.
func mergeOpencodeProvider(doc map[string]any, baseURL, apiKey string, specs map[string]opencodeModelSpec) (added, removed []string, keyUpdated bool) {
	entry := opencodeProviderEntry(doc)
	if _, ok := entry["name"]; !ok {
		entry["name"] = "MyRouter (local)"
	}
	if _, ok := entry["api"]; !ok {
		entry["api"] = "openai"
	}
	opts := opencodeOptions(entry)
	if baseURL != "" {
		opts["baseURL"] = baseURL
	}
	if _, ok := opts["timeout"]; !ok {
		opts["timeout"] = 300000
	}
	if _, ok := opts["chunkTimeout"]; !ok {
		opts["chunkTimeout"] = 300000
	}
	if apiKey != "" {
		opts["apiKey"] = apiKey
		keyUpdated = true
	}
	models := opencodeModels(entry)
	ids := make([]string, 0, len(specs))
	for id := range specs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if _, exists := models[id]; exists {
			continue
		}
		models[id] = opencodeModelEntry(specs[id])
		added = append(added, id)
	}
	for id := range models {
		if _, ok := specs[id]; !ok {
			delete(models, id)
			removed = append(removed, id)
		}
	}
	sort.Strings(removed)
	return added, removed, keyUpdated
}

// writeOpencodeDoc writes the document atomically and keeps a timestamped
// backup of the previous file.
func writeOpencodeDoc(path string, doc map[string]any) (string, error) {
	buf, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return "", err
	}
	buf = append(buf, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", err
	}
	backup := ""
	if _, err := os.Stat(path); err == nil {
		orig, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		backup = fmt.Sprintf("%s.bak-%s", path, time.Now().Format("20060102-150405"))
		if err := os.WriteFile(backup, orig, 0o600); err != nil {
			return "", err
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".opencode-*.json")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.Write(buf); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return "", err
	}
	return backup, nil
}

// opencodeSpecs maps every servable route to the model metadata OpenCode needs.
func (s *Server) opencodeSpecs() (map[string]opencodeModelSpec, error) {
	routes, err := s.db.ListRoutes()
	if err != nil {
		return nil, err
	}
	out := make(map[string]opencodeModelSpec, len(routes))
	for _, rt := range routes {
		if !rt.Enabled || !aliasPattern.MatchString(strings.TrimSpace(rt.Alias)) {
			continue
		}
		usable := false
		for _, t := range rt.Targets {
			if t.Available {
				usable = true
				break
			}
		}
		if !usable {
			continue
		}
		primary, ok := rt.Primary()
		if !ok {
			continue
		}
		mdl, err := s.db.ModelByID(primary.ModelID)
		if err != nil {
			continue
		}
		caps := map[string]bool{}
		for _, c := range store.SplitCSV(mdl.Capabilities) {
			caps[strings.ToLower(c)] = true
		}
		providerName := "MyRouter"
		if p, err := s.db.ProviderByID(mdl.ProviderID); err == nil && p != nil && strings.TrimSpace(p.Name) != "" {
			providerName = strings.TrimSpace(p.Name)
		}
		spec := opencodeModelSpec{
			ID: rt.Alias, Name: rt.Alias + " (" + providerName + ")",
			Vision: caps["vision"], Tools: caps["tools"], Reasoning: caps["reasoning"],
		}
		spec.Context, spec.Output = clampOpencodeLimits(mdl.ContextWindow)
		out[rt.Alias] = spec
	}
	return out, nil
}

// gatewayBaseURL resolves the URL clients should use, preferring the operator's
// custom base URL and otherwise the origin that served this request.
func (s *Server) gatewayBaseURL(r *http.Request) string {
	if v, err := s.db.GetSetting("base_url"); err == nil {
		if v = strings.TrimSpace(v); v != "" {
			return strings.TrimRight(v, "/")
		}
	}
	host := r.Host
	if host == "" {
		host = "127.0.0.1"
		if _, port, err := net.SplitHostPort(s.cfg.Addr); err == nil && port != "" {
			host = net.JoinHostPort("127.0.0.1", port)
		}
	}
	return "http://" + host + "/v1"
}

func (s *Server) handleOpencodeStatus(w http.ResponseWriter, r *http.Request, _ *authSession) {
	path := opencodeConfigPath()
	specs, err := s.opencodeSpecs()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db_error", "could not read routes")
		return
	}
	status := opencodeStatus{
		Path: path, ProviderID: opencodeProviderID, BaseURL: s.gatewayBaseURL(r),
		ListenAddr: s.cfg.Addr, DataDir: s.cfg.DataDir,
		Writable: opencodeWritable(path),
	}
	doc, exists, err := readOpencodeDoc(path)
	if err != nil {
		// The file exists but cannot be parsed: report it instead of failing the
		// whole status call, so the UI can tell the operator what to fix.
		status.Exists = exists
		writeOK(w, map[string]any{"data": status, "error": err.Error()})
		return
	}
	status.Exists = exists
	if providers, ok := doc["provider"].(map[string]any); ok {
		if entry, ok := providers[opencodeProviderID].(map[string]any); ok {
			status.ProviderFound = true
			opts := opencodeOptions(entry)
			if key, ok := opts["apiKey"].(string); ok && strings.TrimSpace(key) != "" {
				status.APIKeyConfigured = true
			}
			for id := range opencodeModels(entry) {
				status.ConfigModels = append(status.ConfigModels, id)
			}
			sort.Strings(status.ConfigModels)
		}
	}
	for id := range specs {
		status.RouteModels = append(status.RouteModels, id)
	}
	sort.Strings(status.RouteModels)
	configured := map[string]bool{}
	for _, id := range status.ConfigModels {
		configured[id] = true
	}
	routed := map[string]bool{}
	for _, id := range status.RouteModels {
		routed[id] = true
		if !configured[id] {
			status.Missing = append(status.Missing, id)
		}
	}
	for _, id := range status.ConfigModels {
		if !routed[id] {
			status.Stale = append(status.Stale, id)
		}
	}
	writeOK(w, map[string]any{"data": status})
}

type opencodeSyncRequest struct {
	APIKey  string `json:"api_key"`
	BaseURL string `json:"base_url"`
}

// handleOpencodeSync merges the current routes (and optionally a freshly issued
// API key) into the OpenCode configuration file.
func (s *Server) handleOpencodeSync(w http.ResponseWriter, r *http.Request, _ *authSession) {
	var req opencodeSyncRequest
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_body", "invalid JSON body")
		return
	}
	req.APIKey = strings.TrimSpace(req.APIKey)
	if req.APIKey != "" && !strings.HasPrefix(req.APIKey, "mr-sk-") {
		writeErr(w, http.StatusBadRequest, "invalid_input", "api_key must be a MyRouter key (mr-sk-...)")
		return
	}
	baseURL := strings.TrimSpace(req.BaseURL)
	if baseURL == "" {
		baseURL = s.gatewayBaseURL(r)
	}
	path := opencodeConfigPath()
	if path == "" {
		writeErr(w, http.StatusInternalServerError, "no_config_path", "could not resolve the OpenCode config directory")
		return
	}
	if !opencodeWritable(path) {
		writeErr(w, http.StatusBadRequest, "read_only_config",
			"opencode.jsonc cannot be rewritten automatically; add the provider block manually or point OPENCODE_CONFIG at a .json file")
		return
	}
	specs, err := s.opencodeSpecs()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db_error", "could not read routes")
		return
	}
	doc, _, err := readOpencodeDoc(path)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_config", err.Error())
		return
	}
	added, removed, keyUpdated := mergeOpencodeProvider(doc, baseURL, req.APIKey, specs)
	backup, err := writeOpencodeDoc(path, doc)
	if err != nil {
		s.log.Error("opencode sync failed", logx.F("path", path), logx.F("error", err.Error()))
		writeErr(w, http.StatusInternalServerError, "write_failed", "could not write the OpenCode config: "+err.Error())
		return
	}
	s.log.Info("opencode config synced",
		logx.F("path", path), logx.F("added", len(added)), logx.F("removed", len(removed)), logx.F("api_key_updated", keyUpdated))
	writeOK(w, map[string]any{"data": map[string]any{
		"path": path, "base_url": baseURL, "added": added, "removed": removed, "pruned": len(removed), "models": len(specs),
		"api_key_updated": keyUpdated, "backup": backup,
	}})
}
