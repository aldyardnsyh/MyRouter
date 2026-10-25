package api

import (
	"net/http"
	"time"
)

// handleLive powers the dashboard routing map: in-flight gateway calls, a short
// recent history, and per-provider activity counts. Read-only and authenticated.
func (s *Server) handleLive(w http.ResponseWriter, r *http.Request, _ *authSession) {
	snap := s.gateway.LiveSnapshot()

	active := make(map[string]int, len(snap.Active))
	for _, req := range snap.Active {
		if req.ProviderID != "" {
			active[req.ProviderID]++
		}
	}

	providers, _ := s.db.ListProviders()
	out := make([]map[string]any, 0, len(providers))
	for _, p := range providers {
		out = append(out, map[string]any{
			"id": p.ID, "name": p.Name, "type": p.Type,
			"status": p.Status, "enabled": p.Enabled,
			"active": active[p.ID],
		})
	}

	writeOK(w, map[string]any{
		"active":      snap.Active,
		"recent":      snap.Recent,
		"providers":   out,
		"server_time": time.Now().UnixMilli(),
	})
}
