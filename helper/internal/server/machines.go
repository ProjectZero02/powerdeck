package server

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/projectzero02/powerdeck/helper/internal/auth"
	"github.com/projectzero02/powerdeck/helper/internal/machines"
)

// ---- KVMs and machines (the hub) ----

func (s *server) noHub(w http.ResponseWriter) bool {
	if s.Machines == nil {
		fail(w, http.StatusNotFound, "not_found", "This helper doesn't drive KVMs.")
		return true
	}
	return false
}

func hubError(w http.ResponseWriter, err error) {
	var e *machines.Error
	if errors.As(err, &e) {
		fail(w, e.Status, e.Code, e.Message)
		return
	}
	fail(w, http.StatusInternalServerError, "internal", err.Error())
}

func (s *server) listKVMs(w http.ResponseWriter, _ *http.Request, _ auth.Caller) {
	if s.noHub(w) {
		return
	}
	out := []machines.KVMView{}
	for _, k := range s.Store.KVMs() {
		out = append(out, machines.View(k))
	}
	writeJSON(w, http.StatusOK, map[string]any{"kvms": out})
}

func (s *server) addKVM(w http.ResponseWriter, r *http.Request, c auth.Caller) {
	if s.noHub(w) {
		return
	}
	var req machines.LinkRequest
	if !decode(r, &req) || req.Address == "" || req.Password == "" {
		fail(w, http.StatusBadRequest, "bad_request", "Send the KVM's address, password and the machine it's wired to.")
		return
	}
	v, led, err := s.Machines.Link(r.Context(), req)
	if err != nil {
		hubError(w, err)
		return
	}
	s.logEvent(c.Name, "link KVM "+v.Name, "wired to "+v.Machine)
	writeJSON(w, http.StatusCreated, map[string]any{"kvm": v, "led": led})
}

func (s *server) patchKVM(w http.ResponseWriter, r *http.Request, c auth.Caller) {
	if s.noHub(w) {
		return
	}
	var p machines.Patch
	if !decode(r, &p) {
		fail(w, http.StatusBadRequest, "bad_request", "Malformed request.")
		return
	}
	v, err := s.Machines.Update(r.Context(), r.PathValue("name"), p)
	if err != nil {
		hubError(w, err)
		return
	}
	if p.Recheck {
		s.logEvent(c.Name, "recheck KVM "+v.Name, "wiring forgotten")
	}
	writeJSON(w, http.StatusOK, map[string]any{"kvm": v})
}

func (s *server) removeKVM(w http.ResponseWriter, r *http.Request, c auth.Caller) {
	if s.noHub(w) {
		return
	}
	if err := s.Machines.Remove(r.PathValue("name")); err != nil {
		hubError(w, err)
		return
	}
	s.logEvent(c.Name, "remove KVM "+r.PathValue("name"), "removed")
	w.WriteHeader(http.StatusNoContent)
}

// listMachines shows the machines the caller may use, with live state.
func (s *server) listMachines(w http.ResponseWriter, r *http.Request, c auth.Caller) {
	if s.noHub(w) {
		return
	}
	out := []machines.Machine{}
	for _, m := range s.Machines.List(r.Context()) {
		names := []string{m.Name}
		if p, err := s.Fleet.Peer(m.Name); err == nil {
			names = peerNames(p)
		}
		if mayUseAny(c, names) {
			out = append(out, m)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"machines": out})
}

type machineRequest struct {
	Force   bool `json:"force"`   // reset or force off although the helper answers
	StayOff bool `json:"stayOff"` // force off without starting again
}

// machineAction runs an action through the same rules as the relay (act).
func (s *server) machineAction(w http.ResponseWriter, r *http.Request, c auth.Caller) {
	var req machineRequest
	if r.ContentLength != 0 && !decode(r, &req) {
		fail(w, http.StatusBadRequest, "bad_request", "Malformed request.")
		return
	}
	action := r.PathValue("action")
	if !slices.Contains(machines.Actions, action) {
		fail(w, http.StatusNotFound, "not_found", "Unknown action "+action+".")
		return
	}
	res, e := s.actWith(r.Context(), c, r.PathValue("name"), action, machines.Options{Force: req.Force, StayOff: req.StayOff})
	if e != nil {
		failErr(w, e)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

// hubAct runs an action through a KVM here; the reply carries the steps (with the power light
// before and after), on failure too.
func (s *server) hubAct(ctx context.Context, c auth.Caller, device, action string, o machines.Options) (map[string]any, *apiError) {
	res := s.Machines.Act(ctx, device, action, o)
	body := map[string]any{}
	for k, v := range res.Helper { // the machine's own reply to shutdown or restart, as without a KVM
		body[k] = v
	}
	for k, v := range map[string]any{"machine": res.Machine, "action": res.Action, "ok": res.OK, "message": res.Message, "steps": res.Steps} {
		body[k] = v
	}
	if res.Code != "" {
		body["code"] = res.Code
	}
	outcome := "done"
	if !res.OK {
		outcome = "refused: " + res.Code
	}
	s.logEvent(c.Name, strings.TrimSpace(action+" "+res.Machine), outcome)
	if !res.OK {
		return nil, &apiError{Status: res.Status, Code: res.Code, Message: res.Message, Extra: body}
	}
	return body, nil
}
