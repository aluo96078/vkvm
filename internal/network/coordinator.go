package network

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"
)

// CoordinatorState is the agent-side discovery/connection state machine. The
// waiting state is intentionally quiescent: it does not run a periodic LAN
// scan and can only be left by a network fingerprint change or an explicit
// rescan request.
type CoordinatorState string

const (
	CoordinatorBoot                    CoordinatorState = "boot"
	CoordinatorDebouncing              CoordinatorState = "debouncing"
	CoordinatorDiscovering             CoordinatorState = "discovering"
	CoordinatorMatching                CoordinatorState = "matching"
	CoordinatorConnecting              CoordinatorState = "connecting"
	CoordinatorActive                  CoordinatorState = "active"
	CoordinatorWaitingForNetworkChange CoordinatorState = "waiting_for_network_change"
)

type PairedEndpoint struct {
	DeviceID          string
	PublicKey         string
	Role              string
	LastKnownEndpoint string
	LastSeenAt        time.Time
}

// TraceEvent is an optional, bounded state-machine evidence record. It is
// intentionally callback-based so normal operation performs no disk I/O.
type TraceEvent struct {
	At     time.Time        `json:"at"`
	State  CoordinatorState `json:"state"`
	Target string           `json:"target,omitempty"`
	Error  string           `json:"error,omitempty"`
}

type CoordinatorConfig struct {
	Paired             []PairedEndpoint
	ConfiguredEndpoint string
	// AllowUnpairedConfiguredEndpoint is an explicit migration escape hatch.
	// Secure/default operation keeps it false so a manually entered address
	// cannot bypass the persistent pairing allowlist.
	AllowUnpairedConfiguredEndpoint bool
	// DiscoveryPort is the API port used by the bounded legacy fallback when
	// mDNS is unavailable. A zero value uses the normal VKVM API default.
	DiscoveryPort     int
	Discovery         func(context.Context) ([]DiscoveredHost, error)
	Probe             func(context.Context, string) error
	Connect           func(context.Context, string) error
	Debounce          time.Duration
	DiscoveryTimeout  time.Duration
	ReconnectAttempts int
	ReconnectBackoff  time.Duration
	Trace             func(TraceEvent)
}

type Coordinator struct {
	cfg CoordinatorConfig

	mu         sync.RWMutex
	state      CoordinatorState
	lastError  error
	lastTarget string
	wake       chan struct{}
	rescan     chan struct{}
}

func NewCoordinator(cfg CoordinatorConfig) *Coordinator {
	if cfg.Debounce <= 0 {
		cfg.Debounce = 2 * time.Second
	}
	if cfg.DiscoveryTimeout <= 0 {
		cfg.DiscoveryTimeout = 1500 * time.Millisecond
	}
	if cfg.ReconnectAttempts <= 0 {
		cfg.ReconnectAttempts = 3
	}
	if cfg.ReconnectBackoff <= 0 {
		cfg.ReconnectBackoff = 250 * time.Millisecond
	}
	if cfg.DiscoveryPort <= 0 {
		cfg.DiscoveryPort = 8080
	}
	if cfg.Discovery == nil {
		cfg.Discovery = func(ctx context.Context) ([]DiscoveredHost, error) {
			return DiscoverVKVM(ctx, cfg.DiscoveryPort)
		}
	}
	return &Coordinator{cfg: cfg, state: CoordinatorBoot, wake: make(chan struct{}, 1), rescan: make(chan struct{}, 1)}
}

func (c *Coordinator) State() CoordinatorState {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.state
}

func (c *Coordinator) LastError() error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lastError
}

func (c *Coordinator) LastTarget() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.lastTarget
}

func (c *Coordinator) setState(state CoordinatorState, err error, target string) {
	c.mu.Lock()
	c.state, c.lastError, c.lastTarget = state, err, target
	trace := c.cfg.Trace
	c.mu.Unlock()
	if trace != nil {
		event := TraceEvent{At: time.Now().UTC(), State: state, Target: target}
		if err != nil {
			event.Error = err.Error()
		}
		trace(event)
	}
}

// Rescan wakes a dormant coordinator without changing its network fingerprint.
func (c *Coordinator) Rescan() {
	select {
	case c.rescan <- struct{}{}:
	default:
	}
}

// Run consumes network fingerprints. The first fingerprint is treated as a
// boot event. A caller may pass nil for fingerprints and use Rescan manually,
// but normal service operation should use NetworkMonitor.Changes(ctx).
func (c *Coordinator) Run(ctx context.Context, fingerprints <-chan NetworkSnapshot) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if fingerprints == nil {
		return errors.New("coordinator requires a network fingerprint source")
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-c.rescan:
			if err := c.resolve(ctx); err != nil && !errors.Is(err, context.Canceled) {
				c.setState(CoordinatorWaitingForNetworkChange, err, c.LastTarget())
			}
		case _, ok := <-fingerprints:
			if !ok {
				return nil
			}
			c.setState(CoordinatorDebouncing, nil, "")
			if err := c.debounce(ctx, fingerprints); err != nil {
				return err
			}
			if err := c.resolve(ctx); err != nil && !errors.Is(err, context.Canceled) {
				c.setState(CoordinatorWaitingForNetworkChange, err, c.LastTarget())
			}
		}
	}
}

func (c *Coordinator) debounce(ctx context.Context, fingerprints <-chan NetworkSnapshot) error {
	timer := time.NewTimer(c.cfg.Debounce)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		case _, ok := <-fingerprints:
			if !ok {
				return ctx.Err()
			}
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(c.cfg.Debounce)
		}
	}
}

func (c *Coordinator) resolve(ctx context.Context) error {
	c.setState(CoordinatorDiscovering, nil, "")
	ctx, cancel := context.WithTimeout(ctx, c.cfg.DiscoveryTimeout)
	defer cancel()
	// Last-known is attempted first and is bounded by the same context as mDNS.
	for _, peer := range c.cfg.Paired {
		if peer.LastKnownEndpoint == "" || !validEndpoint(peer.LastKnownEndpoint) {
			continue
		}
		if c.tryEndpoint(ctx, peer.LastKnownEndpoint) == nil {
			return nil
		}
	}
	if (len(c.cfg.Paired) > 0 || c.cfg.AllowUnpairedConfiguredEndpoint) && c.cfg.ConfiguredEndpoint != "" && validEndpoint(c.cfg.ConfiguredEndpoint) {
		if c.tryEndpoint(ctx, c.cfg.ConfiguredEndpoint) == nil {
			return nil
		}
	}
	c.setState(CoordinatorMatching, nil, "")
	candidates, err := c.cfg.Discovery(ctx)
	if err != nil {
		return err
	}
	target, err := MatchPairedEndpoint(c.cfg.Paired, candidates)
	if err != nil {
		return err
	}
	if err := c.tryEndpoint(ctx, target); err != nil {
		return err
	}
	return nil
}

func (c *Coordinator) tryEndpoint(ctx context.Context, endpoint string) error {
	if c.cfg.Probe != nil {
		if err := c.cfg.Probe(ctx, endpoint); err != nil {
			return err
		}
	}
	c.setState(CoordinatorConnecting, nil, endpoint)
	if c.cfg.Connect == nil {
		c.setState(CoordinatorActive, nil, endpoint)
		return nil
	}
	for attempt := 0; attempt < c.cfg.ReconnectAttempts; attempt++ {
		if err := c.cfg.Connect(ctx, endpoint); err == nil {
			c.setState(CoordinatorActive, nil, endpoint)
			return nil
		} else if attempt == c.cfg.ReconnectAttempts-1 {
			c.setState(CoordinatorWaitingForNetworkChange, err, endpoint)
			return err
		}
		timer := time.NewTimer(c.cfg.ReconnectBackoff * time.Duration(1<<attempt))
		select {
		case <-timer.C:
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return ctx.Err()
		}
	}
	return errors.New("coordinator connection failed")
}

// MatchPairedEndpoint returns exactly one endpoint only when the candidate is
// explicitly paired. Multiple matching devices are rejected so a same-name or
// same-role peer cannot be selected implicitly.
func MatchPairedEndpoint(paired []PairedEndpoint, candidates []DiscoveredHost) (string, error) {
	byID := make(map[string]PairedEndpoint, len(paired))
	for _, peer := range paired {
		if peer.DeviceID != "" {
			byID[peer.DeviceID] = peer
		}
	}
	matched := make([]DiscoveredHost, 0)
	seen := make(map[string]struct{})
	for _, candidate := range candidates {
		peer, ok := byID[candidate.DeviceID]
		// When a paired record pins role or public key, discovery must present
		// the same non-empty value. Accepting an empty candidate here would let a
		// stale/ambiguous mDNS record bypass the identity constraint and defer
		// the rejection until after endpoint selection.
		if !ok || (peer.Role != "" && candidate.Role != peer.Role) || (peer.PublicKey != "" && candidate.PublicKey != peer.PublicKey) || candidate.IP == "" || candidate.Port <= 0 {
			continue
		}
		key := candidate.DeviceID + "@" + net.JoinHostPort(candidate.IP, fmt.Sprint(candidate.Port))
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		matched = append(matched, candidate)
	}
	if len(matched) == 0 {
		return "", errors.New("no paired coordinator discovered")
	}
	// Multiple addresses for one paired UUID are allowed only when they are
	// alternate interfaces of the same device; choose deterministically.
	ids := make(map[string]struct{})
	for _, candidate := range matched {
		ids[candidate.DeviceID] = struct{}{}
	}
	if len(ids) > 1 {
		return "", errors.New("multiple paired coordinators discovered; user selection required")
	}
	sort.Slice(matched, func(i, j int) bool { return matched[i].IP < matched[j].IP })
	return net.JoinHostPort(matched[0].IP, fmt.Sprint(matched[0].Port)), nil
}

func validEndpoint(endpoint string) bool {
	host, port, err := net.SplitHostPort(endpoint)
	if err != nil || strings.TrimSpace(host) == "" || port == "" {
		return false
	}
	return true
}
