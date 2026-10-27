package model

// Skill is a server-side context augmentation applied to gateway requests.
// Skills do not change the model itself; they add instructions or retrieved
// knowledge to the system prompt before the request is forwarded upstream.
type Skill struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Type        string `json:"type"` // prompt | memory | retrieval | chat | vision
	Enabled     bool   `json:"enabled"`
	Config      string `json:"config"` // JSON object, per-type options
	ItemCount   int    `json:"item_count"`
	CreatedAt   int64  `json:"created_at"`

	// Derived fields (not columns): bindings and last sync state.
	Bindings  SkillBindings `json:"bindings"`
	SyncAt    int64         `json:"last_sync_at"`
	SyncError string        `json:"last_sync_error"`
	SyncItems int           `json:"last_sync_items"`
}

// SkillBindings says which routes a skill applies to. Global bindings apply to
// every model alias, so a freshly created route automatically inherits them.
type SkillBindings struct {
	Global bool     `json:"global"`
	Routes []string `json:"routes"`
}

// SkillItem is one knowledge entry owned by a skill. Notes feed memory and chat
// skills; docs feed retrieval skills. Both are injected as text, never executed.
type SkillItem struct {
	ID        string `json:"id"`
	SkillID   string `json:"skill_id"`
	Kind      string `json:"kind"` // note | doc
	Source    string `json:"source"`
	Title     string `json:"title"`
	Content   string `json:"content"`
	CreatedAt int64  `json:"created_at"`
}

const (
	SkillPrompt    = "prompt"
	SkillMemory    = "memory"
	SkillRetrieval = "retrieval"
	SkillChat      = "chat"
	SkillVision    = "vision"
)

// SkillTypes lists every supported skill type (used by API validation).
var SkillTypes = []string{SkillPrompt, SkillMemory, SkillRetrieval, SkillChat, SkillVision}

// ValidSkillType reports whether t is a supported skill type.
func ValidSkillType(t string) bool {
	for _, v := range SkillTypes {
		if v == t {
			return true
		}
	}
	return false
}
