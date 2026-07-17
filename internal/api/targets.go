package api

import (
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"pingway.net/pingway/internal/store"
)

type targetPayload struct {
	Name      string `json:"name"`
	Host      string `json:"host"`
	Tier      int    `json:"tier"`
	SortOrder int    `json:"sort_order"`
	Enabled   *bool  `json:"enabled"`
}

func (p *targetPayload) validate() string {
	p.Name = strings.TrimSpace(p.Name)
	p.Host = strings.TrimSpace(p.Host)
	if p.Name == "" {
		return "name is required"
	}
	if p.Host == "" {
		return "host is required"
	}
	if net.ParseIP(p.Host) == nil {
		// allow hostnames: must be plausible (no spaces, has valid chars)
		if strings.ContainsAny(p.Host, " \t/") {
			return "host must be an IP or hostname"
		}
	}
	if p.Tier < 1 || p.Tier > 3 {
		return "tier must be 1, 2, or 3"
	}
	return ""
}

func (s *Server) handleListTargets(w http.ResponseWriter, r *http.Request) {
	targets, err := s.store.ListTargets(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if targets == nil {
		targets = []store.Target{}
	}
	writeJSON(w, http.StatusOK, targets)
}

func (s *Server) handleCreateTarget(w http.ResponseWriter, r *http.Request) {
	var p targetPayload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json: "+err.Error())
		return
	}
	if msg := p.validate(); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	t := store.Target{
		Name:      p.Name,
		Host:      p.Host,
		Tier:      p.Tier,
		SortOrder: p.SortOrder,
		Enabled:   p.Enabled == nil || *p.Enabled,
	}
	id, err := s.store.CreateTarget(r.Context(), t)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			writeErr(w, http.StatusConflict, "a target with that host already exists")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	t.ID = id
	s.targetsChanged(r)
	writeJSON(w, http.StatusCreated, t)
}

func (s *Server) handleUpdateTarget(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad target id")
		return
	}
	existing, err := s.store.GetTarget(r.Context(), id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if existing == nil {
		writeErr(w, http.StatusNotFound, "target not found")
		return
	}
	var p targetPayload
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeErr(w, http.StatusBadRequest, "bad json: "+err.Error())
		return
	}
	if msg := p.validate(); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	existing.Name = p.Name
	existing.Host = p.Host
	existing.Tier = p.Tier
	existing.SortOrder = p.SortOrder
	if p.Enabled != nil {
		existing.Enabled = *p.Enabled
	}
	if err := s.store.UpdateTarget(r.Context(), *existing); err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			writeErr(w, http.StatusConflict, "a target with that host already exists")
			return
		}
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	s.targetsChanged(r)
	writeJSON(w, http.StatusOK, existing)
}

func (s *Server) handleDeleteTarget(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "bad target id")
		return
	}
	if err := s.store.DeleteTarget(r.Context(), id); err != nil {
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	// close any open outage and drop live state; history is kept
	s.detector.Forget(r.Context(), id, time.Now().UnixMilli())
	s.tracker.Forget(id)
	s.targetsChanged(r)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) targetsChanged(*http.Request) {
	if s.onTargetsChanged != nil {
		s.onTargetsChanged()
	}
}
