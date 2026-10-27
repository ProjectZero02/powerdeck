package server

import (
	"encoding/json"
	"net/http"
	"slices"

	"github.com/projectzero02/powerdeck/helper/internal/auth"
	"github.com/projectzero02/powerdeck/helper/internal/version"
)

// MCP (Model Context Protocol) over streamable HTTP, stateless: each POST is one JSON-RPC request
// answered with one JSON response. Tools map onto act(), so agents get exactly the same rules as
// the REST relay.

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

var mcpVersions = []string{"2025-03-26", "2025-06-18", "2025-11-25", "2026-07-28"}

const deviceArg = `{"type":"string","description":"Device name as shown by list_devices"}`

var mcpTools = json.RawMessage(`[
 {"name":"list_devices","title":"List devices","description":"Lists the PowerDeck devices this key may control, with whether each is on.",
  "inputSchema":{"type":"object","properties":{}},"annotations":{"readOnlyHint":true,"openWorldHint":false}},
 {"name":"get_status","title":"Device status","description":"Shows whether a device is on, its uptime, whether someone is using it, and any pending shutdown.",
  "inputSchema":{"type":"object","properties":{"device":` + deviceArg + `},"required":["device"]},"annotations":{"readOnlyHint":true,"openWorldHint":false}},
 {"name":"power_on","title":"Turn on","description":"Wakes a device that is off (Wake-on-LAN). It usually takes 30-90 seconds to come online.",
  "inputSchema":{"type":"object","properties":{"device":` + deviceArg + `},"required":["device"]},"annotations":{"readOnlyHint":false,"destructiveHint":false,"idempotentHint":true,"openWorldHint":false}},
 {"name":"power_off","title":"Shut down or restart","description":"Shuts down (or restarts) a device after a 60-second warning shown on it, which anyone there can cancel. Refused while someone is using the device.",
  "inputSchema":{"type":"object","properties":{"device":` + deviceArg + `,"restart":{"type":"boolean","description":"Restart instead of shutting down"}},"required":["device"]},
  "annotations":{"readOnlyHint":false,"destructiveHint":true,"idempotentHint":false,"openWorldHint":false}},
 {"name":"cancel","title":"Cancel shutdown","description":"Cancels a pending shutdown or restart.",
  "inputSchema":{"type":"object","properties":{"device":` + deviceArg + `},"required":["device"]},"annotations":{"readOnlyHint":false,"destructiveHint":false,"openWorldHint":false}}
]`)

func (s *server) mcp(w http.ResponseWriter, r *http.Request, c auth.Caller) {
	var req rpcRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.JSONRPC != "2.0" || req.Method == "" {
		writeJSON(w, http.StatusOK, map[string]any{"jsonrpc": "2.0", "id": nil, "error": rpcError{-32700, "parse error"}})
		return
	}
	if req.ID == nil { // a notification: nothing to answer
		w.WriteHeader(http.StatusAccepted)
		return
	}
	result, rerr := s.mcpCall(r, c, req)
	resp := map[string]any{"jsonrpc": "2.0", "id": req.ID}
	if rerr != nil {
		resp["error"] = rerr
	} else {
		resp["result"] = result
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *server) mcpCall(r *http.Request, c auth.Caller, req rpcRequest) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(req.Params, &p)
		v := mcpVersions[len(mcpVersions)-1]
		if slices.Contains(mcpVersions, p.ProtocolVersion) {
			v = p.ProtocolVersion
		}
		return map[string]any{
			"protocolVersion": v,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": "powerdeck", "title": "PowerDeck", "version": version.Version},
			"instructions": "PowerDeck turns the owner's computers on and off. Shutdowns show a 60-second warning on the device " +
				"and are refused while someone is using it; don't retry a refused shutdown, tell the user instead.",
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": mcpTools}, nil
	case "tools/call":
		var p struct {
			Name      string `json:"name"`
			Arguments struct {
				Device  string `json:"device"`
				Restart bool   `json:"restart"`
			} `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, &rpcError{-32602, "invalid params"}
		}
		var (
			out any
			e   *apiError
		)
		switch p.Name {
		case "list_devices":
			out = s.listDevices(r, c)
		case "get_status":
			out, e = s.act(r.Context(), c, p.Arguments.Device, "status")
		case "power_on":
			out, e = s.act(r.Context(), c, p.Arguments.Device, "wake")
		case "power_off":
			action := "shutdown"
			if p.Arguments.Restart {
				action = "restart"
			}
			out, e = s.act(r.Context(), c, p.Arguments.Device, action)
		case "cancel":
			out, e = s.act(r.Context(), c, p.Arguments.Device, "cancel")
		default:
			return nil, &rpcError{-32602, "unknown tool " + p.Name}
		}
		if e != nil {
			return toolResult(map[string]any{"error": e.Code, "message": e.Message}, true), nil
		}
		return toolResult(out, false), nil
	default:
		return nil, &rpcError{-32601, "method not found: " + req.Method}
	}
}

func toolResult(v any, isErr bool) map[string]any {
	b, _ := json.Marshal(v)
	return map[string]any{
		"content":           []map[string]any{{"type": "text", "text": string(b)}},
		"structuredContent": v,
		"isError":           isErr,
	}
}

func (s *server) listDevices(r *http.Request, c auth.Caller) map[string]any {
	type dev struct {
		Name   string `json:"name"`
		OS     string `json:"os,omitempty"`
		Online bool   `json:"online"`
		This   bool   `json:"thisDevice,omitempty"`
	}
	out := []dev{}
	if c.MayUse(s.Name) {
		out = append(out, dev{Name: s.Name, OS: s.OS, Online: true, This: true})
	}
	found, _ := s.Fleet.Found()
	online := map[string]bool{}
	for _, f := range found {
		online[f.Name] = true
	}
	for _, p := range s.Store.Peers() {
		if mayUseAny(c, peerNames(p)) {
			out = append(out, dev{Name: p.Name, OS: p.OS, Online: online[p.Name]})
		}
	}
	return map[string]any{"devices": out}
}
