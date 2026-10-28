package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/projectzero02/powerdeck/helper/internal/auth"
	"github.com/projectzero02/powerdeck/helper/internal/machines"
	"github.com/projectzero02/powerdeck/helper/internal/store"
	"github.com/projectzero02/powerdeck/helper/internal/webhook"
)

// ---- machine events (the hub's watch) ----

// maxStreams bounds open event streams.
const maxStreams = 16

// heartbeat keeps proxies from closing a quiet stream.
var heartbeat = 25 * time.Second

// mayWatch reports whether the caller may see a machine (the same rule as GET /v2/machines).
func (s *server) mayWatch(c auth.Caller, machine string) bool {
	names := []string{machine}
	if p, err := s.Fleet.Peer(machine); err == nil {
		names = peerNames(p)
	}
	return mayUseAny(c, names)
}

// machineEvents streams machine state changes as Server-Sent Events: a snapshot first (or, with
// Last-Event-ID, what came after that event), then each change, with a comment every 25 s.
func (s *server) machineEvents(w http.ResponseWriter, r *http.Request, c auth.Caller) {
	if s.noHub(w) {
		return
	}
	bus := s.Machines.Events
	if bus == nil {
		fail(w, http.StatusNotFound, "not_found", "This helper doesn't watch machines.")
		return
	}
	if s.streams.Add(1) > maxStreams {
		s.streams.Add(-1)
		fail(w, http.StatusServiceUnavailable, "busy", "Too many event streams are open. Try again later.")
		return
	}
	defer s.streams.Add(-1)
	rc := http.NewResponseController(w) // each write sets its own deadline: the server's would end the stream
	ch, stop := bus.Subscribe(64)       // before the replay, so nothing falls in between
	defer stop()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	var last int64
	send := func(name, id string, v any) error {
		b, err := json.Marshal(v)
		if err != nil {
			return err
		}
		_ = rc.SetWriteDeadline(time.Now().Add(10 * time.Second)) // a client that stops reading is dropped
		if id != "" {
			fmt.Fprintf(w, "id: %s\n", id)
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, b); err != nil {
			return err
		}
		return rc.Flush()
	}
	event := func(e machines.Event) error {
		n, _ := strconv.ParseInt(e.ID, 10, 64)
		if n <= last {
			return nil
		}
		last = n
		if !s.mayWatch(c, e.Machine) {
			return nil
		}
		if e.Event == machines.EventRecovery {
			return send("recovery", e.ID, viewEvent(c, e))
		}
		return send("machine", e.ID, e)
	}

	from := r.Header.Get("Last-Event-ID")
	if from == "" {
		from = r.URL.Query().Get("lastEventId") // for clients that can't set headers
	}
	replay, ok := bus.After(from)
	if from == "" || !ok {
		newest := ""
		if recent := bus.Recent(1); len(recent) > 0 {
			newest = recent[0].ID
			last, _ = strconv.ParseInt(newest, 10, 64)
		}
		ms, recs := s.machinesFor(r.Context(), c, true), []machines.Recovery{}
		for _, rec := range s.Machines.Recoveries(true) {
			if s.mayWatch(c, rec.Machine) {
				recs = append(recs, viewRecovery(c, rec))
			}
		}
		if send("snapshot", newest, map[string]any{"machines": ms, "recoveries": recs}) != nil {
			return
		}
	}
	for _, e := range replay {
		if event(e) != nil {
			return
		}
	}
	tick := time.NewTicker(heartbeat)
	defer tick.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-tick.C:
			_ = rc.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if _, err := fmt.Fprint(w, ": still here\n\n"); err != nil || rc.Flush() != nil {
				return
			}
		case e, open := <-ch:
			if !open || event(e) != nil {
				return // fell behind (it reconnects and replays) or went away
			}
		}
	}
}

// note takes a machine's helper's word that it's about to sleep, shut down or restart, or has
// woken: only from a peer key, about the machine it calls from.
func (s *server) note(w http.ResponseWriter, r *http.Request, c auth.Caller) {
	if s.noHub(w) {
		return
	}
	if !c.IsPeer() {
		fail(w, http.StatusForbidden, "scope", "Only a machine's own PowerDeck helper can say what it's about to do.")
		return
	}
	var n machines.Note
	if !decode(r, &n) || !slices.Contains(machines.NoteEvents, n.Event) {
		fail(w, http.StatusBadRequest, "bad_request", "Send {\"event\": \"sleep|shutdown|restart|resume\"}.")
		return
	}
	ip := remoteAddr(r).String()
	for _, p := range s.Store.Peers() {
		if p.Address == ip && s.Machines.HasKVM(p.Name) {
			s.Machines.Note(p.Name, n)
			w.WriteHeader(http.StatusNoContent)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent) // no KVM here for it: nothing to watch
}

// ---- webhooks ----

type webhookView struct {
	ID         string    `json:"id"`
	URL        string    `json:"url"`
	Events     []string  `json:"events"`
	CreatedBy  string    `json:"createdBy,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
	LastAt     time.Time `json:"lastAt,omitzero"`
	LastResult string    `json:"lastResult,omitempty"`
	Secret     string    `json:"secret,omitempty"` // only when it's created
}

func viewWebhook(h store.Webhook) webhookView {
	return webhookView{ID: h.ID, URL: h.URL, Events: h.Events, CreatedBy: h.CreatedBy, CreatedAt: h.CreatedAt,
		LastAt: h.LastAt, LastResult: h.LastResult}
}

func (s *server) listWebhooks(w http.ResponseWriter, _ *http.Request, _ auth.Caller) {
	if s.noHub(w) {
		return
	}
	out := []webhookView{}
	for _, h := range s.Store.Webhooks() {
		out = append(out, viewWebhook(h))
	}
	writeJSON(w, http.StatusOK, map[string]any{"webhooks": out})
}

func (s *server) addWebhook(w http.ResponseWriter, r *http.Request, c auth.Caller) {
	if s.noHub(w) {
		return
	}
	var req struct {
		URL    string   `json:"url"`
		Events []string `json:"events"`
	}
	if !decode(r, &req) {
		fail(w, http.StatusBadRequest, "bad_request", "Malformed request.")
		return
	}
	if err := webhook.CheckURL(req.URL); err != nil {
		fail(w, http.StatusBadRequest, "bad_url", err.Error())
		return
	}
	if len(req.Events) == 0 {
		req.Events = webhook.DefaultEvents
	}
	for _, e := range req.Events {
		if !slices.Contains(machines.EventNames, e) {
			fail(w, http.StatusBadRequest, "bad_event", "Events are "+strings.Join(machines.EventNames, ", ")+".")
			return
		}
	}
	secret := store.NewWebhookSecret()
	sealed, err := s.Machines.Box.Seal(secret)
	if err != nil {
		fail(w, http.StatusInternalServerError, "seal", err.Error())
		return
	}
	h := store.Webhook{ID: store.NewID(), URL: req.URL, Events: slices.Compact(slices.Sorted(slices.Values(req.Events))),
		Secret: sealed, CreatedBy: c.Name, CreatedAt: s.Now().UTC()}
	if err := s.Store.AddWebhook(h); err != nil {
		if errors.Is(err, store.ErrTooManyWebhooks) {
			fail(w, http.StatusConflict, "too_many", fmt.Sprintf("There are %d webhooks already. Remove one first.", store.MaxWebhooks))
			return
		}
		fail(w, http.StatusInternalServerError, "store", err.Error())
		return
	}
	s.logEvent(c.Name, "add webhook "+clean(h.URL, 80), "ok")
	v := viewWebhook(h)
	v.Secret = secret // shown once
	writeJSON(w, http.StatusCreated, map[string]any{"webhook": v})
}

func (s *server) removeWebhook(w http.ResponseWriter, r *http.Request, c auth.Caller) {
	if s.noHub(w) {
		return
	}
	if err := s.Store.RemoveWebhook(r.PathValue("id")); err != nil {
		fail(w, http.StatusNotFound, "not_found", "No such webhook.")
		return
	}
	s.logEvent(c.Name, "remove webhook", "ok")
	w.WriteHeader(http.StatusNoContent)
}

// testWebhook sends one made-up "hung" event (machine "test") now and reports what came back.
func (s *server) testWebhook(w http.ResponseWriter, r *http.Request, _ auth.Caller) {
	if s.noHub(w) {
		return
	}
	if s.Webhooks == nil {
		fail(w, http.StatusNotFound, "not_found", "Webhooks aren't sent from here.")
		return
	}
	i := slices.IndexFunc(s.Store.Webhooks(), func(h store.Webhook) bool { return h.ID == r.PathValue("id") })
	if i < 0 {
		fail(w, http.StatusNotFound, "not_found", "No such webhook.")
		return
	}
	h := s.Store.Webhooks()[i]
	now := s.Now().UTC()
	e := machines.Event{ID: "test-" + strconv.FormatInt(now.Unix(), 10), At: now, Event: machines.StateHung, Machine: "test",
		State: machines.StateHung, From: machines.StateUnknown,
		Evidence: machines.Evidence{At: now, Helper: machines.Silent, Port: machines.Accepts, Ping: machines.NoReply,
			LED: machines.On, LEDTrusted: true}}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	err := s.Webhooks.Send(ctx, h, e)
	result := "ok"
	if err != nil {
		result = err.Error()
	}
	s.Store.WebhookDelivered(h.ID, now, result)
	writeJSON(w, http.StatusOK, map[string]any{"ok": err == nil, "result": result})
}
