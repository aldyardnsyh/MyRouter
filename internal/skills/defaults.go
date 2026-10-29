// Package skills default content: starter prompt + knowledge items per type.
package skills

import (
	"encoding/json"
	"strings"

	"github.com/myrouter/myrouter/internal/model"
	"github.com/myrouter/myrouter/internal/store"
)

// StarterItem is one seeded knowledge entry.
type StarterItem struct {
	Kind    string
	Source  string
	Title   string
	Content string
}

// DefaultContentForType returns the default config patch + starter items for a
// new skill of the given type. Name is used as the role in prompt skills.
func DefaultContentForType(skillType, name string) (configPatch map[string]any, items []StarterItem) {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "skill"
	}
	switch skillType {
	case model.SkillPrompt:
		return map[string]any{
			"prompt": "Bertindak sebagai " + name + ". Jawab sesuai peran ini: gunakan pengetahuan domain yang relevan, jelaskan langkah kerja secara ringkas, dan akhiri dengan satu aksi konkret berikutnya. Jika permintaan di luar peran, katakan batasnya lalu beri alternatif terdekat.",
		}, nil
	case model.SkillMemory:
		return nil, []StarterItem{
			{Kind: "note", Source: "starter", Title: "Cara pakai", Content: "Simpan fakta stabil tentang pengguna/proyek di sini (preferensi, keputusan, konteks). Format: [topik] fakta. Contoh: [deploy] deploy via ./scripts/build-windows.ps1 -Gui."},
			{Kind: "note", Source: "starter", Title: "Contoh format", Content: "Satu note = satu fakta. Judul singkat, isi 1-3 kalimat. Contoh: [api] base URL lokal http://127.0.0.1:20128/v1."},
			{Kind: "note", Source: "starter", Title: "Batasan", Content: "Jangan simpan secret/password/token di sini. Hapus note basi agar injeksi prompt tetap ramping."},
		}
	case model.SkillRetrieval:
		return map[string]any{
			"sync_paths": []string{"docs/"},
			"sync_glob":  "*.md",
		}, []StarterItem{
			{Kind: "doc", Source: "starter/contoh-arsitektur.md", Title: "Contoh dokumen arsitektur", Content: "# Contoh Arsitektur\n\nFormat yang diharapkan: heading jelas, satu topik per file. File .md di sync_paths akan di-ingest otomatis via tombol Sync."},
			{Kind: "doc", Source: "starter/contoh-api.md", Title: "Contoh dokumen API", Content: "# Contoh API\n\nGET /v1/models — daftar model.\nPOST /v1/chat/completions — chat. Panduan sync_paths: isi dengan folder docs/, atur sync_glob misal *.md."},
		}
	case model.SkillChat:
		return nil, []StarterItem{
			{Kind: "note", Source: "starter", Title: "Capture otomatis", Content: "Skill chat menangkap pertukaran penting otomatis (capture_max_chars/capture_keep di config). Hapus contoh ini setelah capture pertama berjalan."},
			{Kind: "note", Source: "starter", Title: "Contoh exchange", Content: "User: bagaimana deploy? Asisten: jalankan ./scripts/build-windows.ps1 -Gui lalu cek dist/MyRouter.exe. Format capture: ringkasan Q/A, bukan transkrip penuh."},
		}
	case model.SkillVision:
		return map[string]any{
			"vision_prompt": "Deskripsikan gambar secara detail dan terstruktur: objek utama, teks yang terlihat (OCR), tata letak/warna, lalu satu kalimat konteks untuk pertanyaan pengguna.",
		}, nil
	default:
		return nil, nil
	}
}

// SeedDefaults fills empty prompt/vision config + inserts starter items.
// Never overwrites user-supplied values; no-op when skill already has content.
func SeedDefaults(db *store.DB, sk *model.Skill) error {
	patch, items := DefaultContentForType(sk.Type, sk.Name)
	cfg := map[string]any{}
	if raw := strings.TrimSpace(sk.Config); raw != "" {
		_ = json.Unmarshal([]byte(raw), &cfg)
	}
	changed := false
	for k, v := range patch {
		if cur, ok := cfg[k]; !ok || strings.TrimSpace(jsonStr(cur)) == "" {
			cfg[k] = v
			changed = true
		}
	}
	if changed {
		raw, err := json.Marshal(cfg)
		if err != nil {
			return err
		}
		sk.Config = string(raw)
		if err := db.UpdateSkill(sk); err != nil {
			return err
		}
	}
	if len(items) == 0 {
		return nil
	}
	existing, err := db.ListSkillItems(sk.ID, 1)
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		return nil // user already added items
	}
	seed := make([]model.SkillItem, 0, len(items))
	for _, it := range items {
		seed = append(seed, model.SkillItem{
			ID: store.NewID("ski"), SkillID: sk.ID, Kind: it.Kind,
			Source: it.Source, Title: it.Title, Content: it.Content,
			CreatedAt: store.Now(),
		})
	}
	return db.CreateSkillItems(seed)
}

func jsonStr(v any) string {
	s, _ := v.(string)
	return s
}
