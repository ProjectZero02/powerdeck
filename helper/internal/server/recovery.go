package server

import (
	"net/http"

	"github.com/projectzero02/powerdeck/helper/internal/auth"
	"github.com/projectzero02/powerdeck/helper/internal/fleet"
	"github.com/projectzero02/powerdeck/helper/internal/machines"
	"github.com/projectzero02/powerdeck/helper/internal/store"
)

// ---- recoveries (the hub): proposed by anyone allowed, confirmed by the owner ----

// viewRecovery hides which key proposed it from everyone but the owner and that key.
func viewRecovery(c auth.Caller, r machines.Recovery) machines.Recovery {
	if c.Kind != auth.Owner && (c.Key == nil || c.Key.ID != r.ProposerKey) {
		r.ProposerKey = ""
	}
	return r
}

// ownerHere is an owner device acting for itself: not an agent, not a hub relaying for one.
func ownerHere(c auth.Caller) bool { return c.Kind == auth.Owner && !c.ForAgent() }

func (s *server) proposeRecovery(w http.ResponseWriter, r *http.Request, c auth.Caller) {
	if s.noHub(w) {
		return
	}
	var req struct {
		Action string `json:"action"` // reset (the default) or force-off
		Reason string `json:"reason"`
	}
	if !decode(r, &req) {
		fail(w, http.StatusBadRequest, "bad_request", "Send {\"action\": \"reset|force-off\", \"reason\": \"...\"}.")
		return
	}
	if req.Action == "" {
		req.Action = "reset"
	}
	if !c.Has(store.ScopePower) { // not force: agent keys can't hold it, and the owner confirms anyway
		fail(w, http.StatusForbidden, "scope", "This key isn't allowed to propose a reset or force off (it needs the power scope).")
		return
	}
	device := r.PathValue("name")
	if fleet.IsSelf(device, s.Name) {
		fail(w, http.StatusUnprocessableEntity, "self", s.Name+" can't reset itself.")
		return
	}
	names := []string{device}
	if p, err := s.Fleet.Peer(device); err == nil {
		device, names = p.Name, peerNames(p)
	}
	if !mayUseAny(c, append(names, device)) || c.Kind != auth.Owner && !c.IsAutomation() {
		fail(w, http.StatusForbidden, "device", "This key can't control "+device+".")
		return
	}
	if c.ForAgent() {
		if e := s.agentsAllowed(r.Context(), device, false); e != nil {
			failErr(w, e)
			return
		}
	}
	p := machines.Proposal{Machine: device, Action: req.Action, Reason: req.Reason, By: c.Name, Agent: c.ForAgent()}
	if c.Key != nil {
		p.Key = c.Key.ID
	}
	rec, created, err := s.Machines.Propose(p)
	if err != nil {
		hubError(w, err)
		return
	}
	status := http.StatusOK // one was open already
	if created {
		status = http.StatusCreated
	}
	writeJSON(w, status, map[string]any{"recovery": viewRecovery(c, rec)})
}

func (s *server) listRecoveries(w http.ResponseWriter, r *http.Request, c auth.Caller) {
	if s.noHub(w) {
		return
	}
	out := []machines.Recovery{}
	for _, rec := range s.Machines.Recoveries(r.URL.Query().Get("open") == "1") {
		if s.mayWatch(c, rec.Machine) {
			out = append(out, viewRecovery(c, rec))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"recoveries": out})
}

// recovery finds the recovery in the path, if the caller may see it.
func (s *server) recovery(w http.ResponseWriter, r *http.Request, c auth.Caller) (machines.Recovery, bool) {
	if s.noHub(w) {
		return machines.Recovery{}, false
	}
	rec, ok := s.Machines.Recovery(r.PathValue("id"))
	if !ok || !s.mayWatch(c, rec.Machine) {
		fail(w, http.StatusNotFound, "not_found", "No such recovery.")
		return machines.Recovery{}, false
	}
	return rec, true
}

func (s *server) getRecovery(w http.ResponseWriter, r *http.Request, c auth.Caller) {
	if rec, ok := s.recovery(w, r, c); ok {
		writeJSON(w, http.StatusOK, map[string]any{"recovery": viewRecovery(c, rec)})
	}
}

// confirmRecovery runs a proposed recovery: owner devices only. It answers at once (202); the
// recovery's events and GET /v2/recoveries/{id} tell how it went.
func (s *server) confirmRecovery(w http.ResponseWriter, r *http.Request, c auth.Caller) {
	if _, ok := s.recovery(w, r, c); !ok {
		return
	}
	if !ownerHere(c) {
		fail(w, http.StatusForbidden, "owner_only", "Only the owner's devices can confirm a reset or force off.")
		return
	}
	rec, err := s.Machines.Confirm(r.PathValue("id"), c.Name)
	if err != nil {
		hubError(w, err)
		return
	}
	status := http.StatusOK // running or over already: as it is
	if rec.State == machines.RecoveryRunning && rec.ConfirmedBy == c.Name {
		status = http.StatusAccepted
	}
	writeJSON(w, status, map[string]any{"recovery": viewRecovery(c, rec)})
}

// declineRecovery closes a proposed recovery: the owner declines it, or its proposer withdraws it.
func (s *server) declineRecovery(w http.ResponseWriter, r *http.Request, c auth.Caller) {
	rec, ok := s.recovery(w, r, c)
	if !ok {
		return
	}
	own := c.Key != nil && rec.ProposerKey != "" && c.Key.ID == rec.ProposerKey
	if !own && !ownerHere(c) {
		fail(w, http.StatusForbidden, "owner_only", "Only the owner's devices, or the key that proposed it, can decline it.")
		return
	}
	rec, err := s.Machines.Decline(rec.ID, c.Name, own)
	if err != nil {
		hubError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"recovery": viewRecovery(c, rec)})
}
