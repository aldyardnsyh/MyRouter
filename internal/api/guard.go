package api

import (
	"net"
	"net/http"
	"strings"

	"github.com/myrouter/myrouter/internal/model"
	"github.com/myrouter/myrouter/internal/store"
)

// ---------------------------------------------------------------------------
// Policies
// ---------------------------------------------------------------------------

func (s *Server) handleListPolicies(w http.ResponseWriter, r *http.Request, _ *authSession) {
	list, err := s.db.ListPolicies()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeOK(w, map[string]any{"data": list})
}

type policyPayload struct {
	Name          string   `json:"name"`
	RPM           *int     `json:"rpm"`
	TPM           *int     `json:"tpm"`
	Burst         *int     `json:"burst"`
	DailyTokens   *int     `json:"daily_tokens"`
	MonthlyTokens *int     `json:"monthly_tokens"`
	MaxConcurrent *int     `json:"max_concurrent"`
	MaxTokensReq  *int     `json:"max_tokens_per_request"`
	MaxIPs        *int     `json:"max_ips"`
	MaxDevices    *int     `json:"max_devices"`
	AllowedModels []string `json:"allowed_models"`
	OnExceed      string   `json:"on_exceed"`
	QueueMaxS     *int     `json:"queue_max_s"`
	CooldownS     *int     `json:"cooldown_s"`
	AutoBlockN    *int     `json:"auto_block_n"`
	AutoBlockM    *int     `json:"auto_block_m"`
	AutoBlockS    *int     `json:"auto_block_s"`
}

func applyInt(dst *int, src *int) {
	if src != nil && *src >= 0 {
		*dst = *src
	}
}

func (p policyPayload) apply(pl *model.Policy) {
	if p.Name != "" {
		pl.Name = strings.TrimSpace(p.Name)
	}
	applyInt(&pl.RPM, p.RPM)
	applyInt(&pl.TPM, p.TPM)
	applyInt(&pl.Burst, p.Burst)
	applyInt(&pl.DailyTokens, p.DailyTokens)
	applyInt(&pl.MonthlyTokens, p.MonthlyTokens)
	applyInt(&pl.MaxConcurrent, p.MaxConcurrent)
	applyInt(&pl.MaxTokensReq, p.MaxTokensReq)
	applyInt(&pl.MaxIPs, p.MaxIPs)
	applyInt(&pl.MaxDevices, p.MaxDevices)
	applyInt(&pl.QueueMaxS, p.QueueMaxS)
	applyInt(&pl.CooldownS, p.CooldownS)
	applyInt(&pl.AutoBlockN, p.AutoBlockN)
	applyInt(&pl.AutoBlockM, p.AutoBlockM)
	applyInt(&pl.AutoBlockS, p.AutoBlockS)
	if p.OnExceed != "" {
		switch p.OnExceed {
		case "reject", "queue", "fallback":
			pl.OnExceed = p.OnExceed
		}
	}
	if p.AllowedModels != nil {
		pl.AllowedModels = store.JoinCSV(p.AllowedModels)
	}
	if pl.OnExceed == "" {
		pl.OnExceed = "reject"
	}
}

func (s *Server) handleCreatePolicy(w http.ResponseWriter, r *http.Request, _ *authSession) {
	var req policyPayload
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_body", "invalid JSON body")
		return
	}
	if strings.TrimSpace(req.Name) == "" {
		writeErr(w, http.StatusBadRequest, "invalid_input", "policy name is required")
		return
	}
	pl := &model.Policy{}
	req.apply(pl)
	if err := s.db.UpsertPolicy(pl); err != nil {
		writeErr(w, http.StatusInternalServerError, "create_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"data": pl})
}

func (s *Server) handleUpdatePolicy(w http.ResponseWriter, r *http.Request, _ *authSession) {
	pl, err := s.db.PolicyByID(s.pathID(r))
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "policy not found")
		return
	}
	var req policyPayload
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_body", "invalid JSON body")
		return
	}
	req.apply(pl)
	if err := s.db.UpsertPolicy(pl); err != nil {
		writeErr(w, http.StatusInternalServerError, "update_failed", err.Error())
		return
	}
	writeOK(w, map[string]any{"data": pl})
}

func (s *Server) handleDeletePolicy(w http.ResponseWriter, r *http.Request, _ *authSession) {
	if err := s.db.DeletePolicy(s.pathID(r)); err != nil {
		writeErr(w, http.StatusInternalServerError, "delete_failed", err.Error())
		return
	}
	writeOK(w, map[string]bool{"ok": true})
}

// ---------------------------------------------------------------------------
// IP rules
// ---------------------------------------------------------------------------

func (s *Server) handleListIPRules(w http.ResponseWriter, r *http.Request, _ *authSession) {
	list, err := s.db.ListIPRules()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeOK(w, map[string]any{"data": list})
}

type ipRulePayload struct {
	CIDR      string `json:"cidr"`
	Action    string `json:"action"`
	ScopeType string `json:"scope_type"`
	ScopeID   string `json:"scope_id"`
	Note      string `json:"note"`
	ExpiresAt *int64 `json:"expires_at"`
}

func (s *Server) handleCreateIPRule(w http.ResponseWriter, r *http.Request, _ *authSession) {
	var req ipRulePayload
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_body", "invalid JSON body")
		return
	}
	cidr := strings.TrimSpace(req.CIDR)
	if cidr == "" {
		writeErr(w, http.StatusBadRequest, "invalid_input", "cidr is required")
		return
	}
	if _, _, err := net.ParseCIDR(normalizeCIDR(cidr)); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_cidr", "invalid CIDR or IP address")
		return
	}
	if req.Action != "allow" && req.Action != "deny" {
		writeErr(w, http.StatusBadRequest, "invalid_input", "action must be allow or deny")
		return
	}
	if req.ScopeType == "" {
		req.ScopeType = "global"
	}
	if req.ScopeType != "global" && req.ScopeType != "api_key" {
		writeErr(w, http.StatusBadRequest, "invalid_input", "scope_type must be global or api_key")
		return
	}
	rule := &model.IPRule{
		ScopeType: req.ScopeType, ScopeID: req.ScopeID, CIDR: cidr, Action: req.Action,
		Note: strings.TrimSpace(req.Note), ExpiresAt: req.ExpiresAt,
	}
	if err := s.db.CreateIPRule(rule); err != nil {
		writeErr(w, http.StatusInternalServerError, "create_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"data": rule})
}

func (s *Server) handleDeleteIPRule(w http.ResponseWriter, r *http.Request, _ *authSession) {
	if err := s.db.DeleteIPRule(s.pathID(r)); err != nil {
		writeErr(w, http.StatusInternalServerError, "delete_failed", err.Error())
		return
	}
	writeOK(w, map[string]bool{"ok": true})
}

// ---------------------------------------------------------------------------
// Devices
// ---------------------------------------------------------------------------

func (s *Server) handleListDevices(w http.ResponseWriter, r *http.Request, _ *authSession) {
	list, err := s.db.ListDevices()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	writeOK(w, map[string]any{"data": list})
}

type blockDevicePayload struct {
	Blocked *bool  `json:"blocked"`
	Reason  string `json:"reason"`
}

func (s *Server) handleBlockDevice(w http.ResponseWriter, r *http.Request, _ *authSession) {
	dev, err := s.db.DeviceByID(s.pathID(r))
	if err != nil {
		writeErr(w, http.StatusNotFound, "not_found", "device not found")
		return
	}
	var req blockDevicePayload
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_body", "invalid JSON body")
		return
	}
	blocked := true
	if req.Blocked != nil {
		blocked = *req.Blocked
	}
	if err := s.db.SetDeviceBlocked(dev.ID, blocked); err != nil {
		writeErr(w, http.StatusInternalServerError, "update_failed", err.Error())
		return
	}
	if blocked {
		reason := req.Reason
		if reason == "" {
			reason = "device blocked by admin"
		}
		// Avoid duplicate block entries when the operator clicks Block twice.
		existing, _ := s.db.ListBlocks()
		already := false
		for _, b := range existing {
			if b.Kind == "device" && b.Value == dev.ID {
				already = true
				break
			}
		}
		if !already {
			_ = s.db.CreateBlock(&model.BlockEntry{Kind: "device", Value: dev.ID, ScopeID: dev.APIKeyID, Reason: reason})
		}
	} else {
		// Unblocking must also drop the block entry: Guard checks block entries
		// independently of the device.blocked flag, so leaving it behind kept
		// returning 403 while the Devices tab looked "Active".
		existing, _ := s.db.ListBlocks()
		for _, b := range existing {
			if b.Kind == "device" && b.Value == dev.ID {
				_ = s.db.DeleteBlock(b.ID)
			}
		}
	}
	writeOK(w, map[string]any{"data": dev, "blocked": blocked})
}

type renamePayload struct {
	Label string `json:"label"`
}

func (s *Server) handleRenameDevice(w http.ResponseWriter, r *http.Request, _ *authSession) {
	var req renamePayload
	if err := decode(r, &req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid_body", "invalid JSON body")
		return
	}
	if strings.TrimSpace(req.Label) == "" {
		writeErr(w, http.StatusBadRequest, "invalid_input", "label is required")
		return
	}
	if err := s.db.RenameDevice(s.pathID(r), strings.TrimSpace(req.Label)); err != nil {
		writeErr(w, http.StatusInternalServerError, "update_failed", err.Error())
		return
	}
	writeOK(w, map[string]bool{"ok": true})
}

// ---------------------------------------------------------------------------
// Blocked
// ---------------------------------------------------------------------------

func (s *Server) handleListBlocked(w http.ResponseWriter, r *http.Request, _ *authSession) {
	list, err := s.db.ListBlocks()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db_error", err.Error())
		return
	}
	// Prune expired entries from the response.
	now := store.Now()
	active := make([]model.BlockEntry, 0, len(list))
	for _, b := range list {
		if b.Until != nil && *b.Until <= now {
			continue
		}
		active = append(active, b)
	}
	writeOK(w, map[string]any{"data": active})
}

// handleUnblock removes a block entry and unblocks a matching device.
func (s *Server) handleUnblock(w http.ResponseWriter, r *http.Request, _ *authSession) {
	id := s.pathID(r)
	blocks, _ := s.db.ListBlocks()
	for _, b := range blocks {
		if b.ID == id && b.Kind == "device" {
			_ = s.db.SetDeviceBlocked(b.Value, false)
		}
	}
	if err := s.db.DeleteBlock(id); err != nil {
		writeErr(w, http.StatusInternalServerError, "delete_failed", err.Error())
		return
	}
	writeOK(w, map[string]bool{"ok": true})
}

func normalizeCIDR(s string) string {
	s = strings.TrimSpace(s)
	if strings.Contains(s, "/") {
		return s
	}
	if strings.Contains(s, ":") {
		return s + "/128"
	}
	return s + "/32"
}
