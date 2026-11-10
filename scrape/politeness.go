package scrape

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Politeness is how the engine treats each site's server (the Kotlin Politeness.kt): one request at a time, at least
// Gap after the previous one ended, and a pause when the site pushes back (Pushback: a 429, a 403, a server error, a
// block page), FirstPause at first, doubling while it keeps pushing back, at most MaxPause, or as long as the site's
// Retry-After asks. While a site is paused its requests fail at once (SitePausedError) without reaching it. A success
// ends the doubling. A site can have its own gap instead of Gap (SetGaps).
type Politeness struct {
	Gap        time.Duration
	FirstPause time.Duration
	MaxPause   time.Duration
	// The clock and the wait; tests replace both with virtual time.
	Now   func() time.Time
	Sleep func(ctx context.Context, d time.Duration) error

	mu    sync.Mutex
	hosts map[string]*hostState
	gaps  map[string]time.Duration
}

type hostState struct {
	turn        sync.Mutex
	lastEnd     time.Time
	pausedUntil time.Time
	strikes     int
}

// NewPoliteness has the app's defaults: a 250 ms gap, a first pause of 2 minutes, at most an hour.
func NewPoliteness() *Politeness {
	return &Politeness{Gap: 250 * time.Millisecond, FirstPause: 2 * time.Minute, MaxPause: time.Hour, Now: time.Now, Sleep: sleep}
}

func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// SetGaps gives each host in byHost its own gap between requests (any "www." ignored); the others keep Gap.
func (p *Politeness) SetGaps(byHost map[string]time.Duration) {
	gaps := make(map[string]time.Duration, len(byHost))
	for host, gap := range byHost {
		gaps[strings.TrimPrefix(host, "www.")] = gap
	}
	p.mu.Lock()
	p.gaps = gaps
	p.mu.Unlock()
}

func (p *Politeness) host(name string) *hostState {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.hosts == nil {
		p.hosts = map[string]*hostState{}
	}
	state := p.hosts[name]
	if state == nil {
		state = &hostState{}
		p.hosts[name] = state
	}
	return state
}

func (p *Politeness) gapOf(host string) time.Duration {
	p.mu.Lock()
	defer p.mu.Unlock()
	if gap, ok := p.gaps[strings.TrimPrefix(host, "www.")]; ok {
		return gap
	}
	return p.Gap
}

// Request runs request to host in its turn: after the one before it and the gap, unless the host is paused.
func (p *Politeness) Request(ctx context.Context, host string, request func() error) error {
	state := p.host(host)
	state.turn.Lock()
	defer state.turn.Unlock()
	p.mu.Lock()
	paused := state.pausedUntil.Sub(p.Now())
	p.mu.Unlock()
	if paused > 0 {
		return &SitePausedError{Host: host, Remaining: paused}
	}
	if !state.lastEnd.IsZero() {
		if wait := state.lastEnd.Add(p.gapOf(host)).Sub(p.Now()); wait > 0 {
			if err := p.Sleep(ctx, wait); err != nil {
				return err
			}
		}
	}
	defer func() { state.lastEnd = p.Now() }()
	return request()
}

// Success says host answered as it should: the next pushback starts from FirstPause again.
func (p *Politeness) Success(host string) {
	state := p.host(host)
	p.mu.Lock()
	state.strikes = 0
	p.mu.Unlock()
}

// Pushback pauses host for the next step of the doubling, or retryAfter (0: none) when it asked for longer.
func (p *Politeness) Pushback(host string, retryAfter time.Duration) {
	state := p.host(host)
	p.mu.Lock()
	defer p.mu.Unlock()
	doubled := p.FirstPause * time.Duration(1<<min(state.strikes, 10))
	pause := max(min(doubled, p.MaxPause), min(retryAfter, p.MaxPause))
	state.strikes++
	state.pausedUntil = p.Now().Add(pause)
}

// PausedFor is how long host is still paused; zero when it isn't.
func (p *Politeness) PausedFor(host string) time.Duration {
	state := p.host(host)
	p.mu.Lock()
	defer p.mu.Unlock()
	return max(state.pausedUntil.Sub(p.Now()), 0)
}

// SitePausedError is a request to Host not made: the site pushed back and is paused for Remaining more.
type SitePausedError struct {
	Host      string
	Remaining time.Duration
}

func (e *SitePausedError) Error() string {
	return fmt.Sprintf("%s is paused for %ds after it pushed back", e.Host, e.Remaining/time.Second)
}
