// Package auth decides who is calling and what they may do (see docs/protocol.md "Identity and
// trust"): owners are recognised by their Tailscale identity, everyone else needs a key.
package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/netip"
	"slices"
	"strings"
	"time"

	"github.com/projectzero02/powerdeck/helper/internal/store"
	"github.com/projectzero02/powerdeck/helper/internal/tailscale"
)

// Caller kinds.
const (
	Owner  = "owner"
	Holder = "key"
	Nobody = "none"
)

// Caller is an authenticated request's identity.
type Caller struct {
	Kind   string
	Name   string // for logs and the activity history, e.g. "OPPO Find X9 Ultra" or "Lyra"
	NodeID string
	Node   string // short MagicDNS name of the caller's node
	Scopes []string
	Key    *store.Key // set for key holders
}

// Has reports whether the caller has a scope.
func (c Caller) Has(scope string) bool { return slices.Contains(c.Scopes, scope) }

// IsAutomation reports whether the caller uses an automation key.
func (c Caller) IsAutomation() bool { return c.Key != nil && c.Key.Kind == store.KindAutomation }

// MayUse reports whether the caller may act on the named device (relay).
func (c Caller) MayUse(device string) bool { return c.Key == nil || c.Key.MayUse(device) }

// Identifier resolves tailnet identities.
type Identifier interface {
	WhoIs(ctx context.Context, ip netip.Addr) (tailscale.Identity, error)
	Status(ctx context.Context) (*tailscale.Status, error)
}

// Authenticator resolves callers.
type Authenticator struct {
	TS    Identifier
	Store *store.Store
	Now   func() time.Time
	// LocalToken lets this machine's own tools (CLI, PC window) act as owner over loopback. It's
	// written to the data directory, which only Administrators/root can read.
	LocalToken string
}

// ErrBadKey means a key was presented but isn't valid (or is used from the wrong node).
var ErrBadKey = errors.New("unknown or revoked key")

// ErrUnavailable means Tailscale couldn't say who is calling (it may be restarting); try again.
var ErrUnavailable = errors.New("tailscale identity unavailable")

// Resolve identifies the caller at ip, presenting bearer (may be empty). Loopback callers count as
// owner only with the local token; any other local program is nobody.
func (a *Authenticator) Resolve(ctx context.Context, ip netip.Addr, bearer string) (Caller, error) {
	if ip.IsLoopback() {
		if a.LocalToken != "" && subtle.ConstantTimeCompare([]byte(bearer), []byte(a.LocalToken)) == 1 {
			return Caller{Kind: Owner, Name: "this device", NodeID: "local", Scopes: store.AllScopes}, nil
		}
		if !strings.HasPrefix(bearer, store.KeyPrefix) {
			return Caller{Kind: Nobody}, nil
		}
	}
	id, idErr := a.TS.WhoIs(ctx, ip)
	if bearer != "" {
		k, ok := a.Store.Authenticate(bearer, a.Now())
		if !ok {
			return Caller{Kind: Nobody}, ErrBadKey
		}
		if k.Kind == store.KindAutomation {
			// Automation keys are bound to the node that first uses them.
			if idErr != nil || id.NodeID == "" {
				return Caller{Kind: Nobody}, ErrBadKey
			}
			if k.BoundNode == "" {
				_ = a.Store.BindKey(k.ID, id.NodeID)
				k.BoundNode = id.NodeID
			}
			if k.BoundNode != id.NodeID {
				return Caller{Kind: Nobody}, ErrBadKey
			}
		}
		return Caller{Kind: Holder, Name: k.Name, NodeID: id.NodeID, Node: id.NodeName, Scopes: k.Scopes, Key: &k}, nil
	}
	if idErr == nil && a.isOwner(ctx, id) {
		name := id.Hostname
		if name == "" {
			name = id.NodeName
		}
		return Caller{Kind: Owner, Name: name, NodeID: id.NodeID, Node: id.NodeName, Scopes: store.AllScopes}, nil
	}
	if idErr != nil {
		// An address Tailscale doesn't know is nobody; Tailscale itself not answering is retryable.
		if _, err := a.TS.Status(ctx); err != nil {
			return Caller{Kind: Nobody}, ErrUnavailable
		}
	}
	return Caller{Kind: Nobody, NodeID: id.NodeID}, nil
}

// isOwner: an untagged node, not shared in, owned by the same user as this (untagged) device.
func (a *Authenticator) isOwner(ctx context.Context, id tailscale.Identity) bool {
	if id.Tagged || id.Shared || id.UserID == 0 {
		return false
	}
	st, err := a.TS.Status(ctx)
	if err != nil || len(st.Self.Tags) > 0 || st.Self.UserID == 0 {
		return false
	}
	return st.Self.UserID == id.UserID
}

// ParseScopes validates a comma-separated scope list for an automation key. Automation keys can't
// hold force or admin.
func ParseScopes(s string) ([]string, error) {
	if strings.TrimSpace(s) == "" {
		return []string{store.ScopeStatus, store.ScopeWake}, nil
	}
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(strings.ToLower(p))
		switch p {
		case store.ScopeStatus, store.ScopeWake, store.ScopePower:
			if !slices.Contains(out, p) {
				out = append(out, p)
			}
		case store.ScopeForce, store.ScopeAdmin:
			return nil, errors.New("automation keys can't have the " + p + " scope")
		default:
			return nil, errors.New("unknown scope " + p + " (use status, wake, power)")
		}
	}
	if !slices.Contains(out, store.ScopeStatus) {
		out = append([]string{store.ScopeStatus}, out...)
	}
	return out, nil
}
