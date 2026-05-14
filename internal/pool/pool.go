// Package pool maintains a warm pool of runner pods (agent-sandbox CRs) for
// vibeD's Instant Preview fast path. Each language has a sub-pool of idle pods
// kept topped up ahead of demand; a deploy Claims one, the RunnerDeployer
// injects source into it, and Release recycles it — the pod is deleted, never
// handed to another tenant. When the pool is empty Claim falls back to creating
// a cold runner on demand so deploys never fail just because demand spiked.
package pool

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/vibed-project/vibeD/internal/config"
	"github.com/vibed-project/vibeD/internal/metrics"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
)

// ErrLanguageUnsupported is returned by Claim for a language with no runner
// configured in fastPath.runners.
var ErrLanguageUnsupported = errors.New("pool: language not supported")

// probeFunc reports whether a runner agent's control API is reachable.
type probeFunc func(ctx context.Context, controlURL string) error

// Pool maintains warm runner pods, one sub-pool per language.
type Pool struct {
	dyn    dynamic.Interface
	cfg    config.FastPathConfig
	ns     string
	m      *metrics.Metrics
	logger *slog.Logger
	probe  probeFunc

	// runCtx is the pool's lifecycle context, set by Run. Background work
	// (replenishment triggered by Claim/Release) derives from this rather than
	// from a single request's context.
	runCtx context.Context

	mu      sync.Mutex
	idle    map[string][]*Runner // language -> ready, claimable runners
	pending map[string]int       // language -> in-flight warmups
	tracked map[string]*Runner   // sandbox name -> every runner CR the pool owns
}

// New constructs a Pool. ns is the fallback namespace used when
// cfg.Namespace is empty.
func New(dyn dynamic.Interface, cfg config.FastPathConfig, ns string, m *metrics.Metrics, logger *slog.Logger) *Pool {
	if cfg.Namespace != "" {
		ns = cfg.Namespace
	}
	return &Pool{
		dyn:     dyn,
		cfg:     cfg,
		ns:      ns,
		m:       m,
		logger:  logger,
		probe:   httpProbe,
		runCtx:  context.Background(),
		idle:    map[string][]*Runner{},
		pending: map[string]int{},
		tracked: map[string]*Runner{},
	}
}

// Supports reports whether the pool has a runner configured for language.
func (p *Pool) Supports(language string) bool {
	_, ok := p.cfg.Runners[language]
	return ok
}

// Languages returns the languages the pool serves.
func (p *Pool) Languages() []string {
	out := make([]string, 0, len(p.cfg.Runners))
	for lang := range p.cfg.Runners {
		out = append(out, lang)
	}
	return out
}

// IdleCount returns the number of warm idle runners for language.
func (p *Pool) IdleCount(language string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.idle[language])
}

// Run starts the pool: an initial fill, then a replenish loop and a periodic
// health/age sweep, until ctx is cancelled — at which point every runner CR
// the pool owns is deleted. It is a no-op when the fast path is disabled.
func (p *Pool) Run(ctx context.Context) {
	if !p.cfg.Enabled {
		return
	}
	p.runCtx = ctx
	p.logger.Info("runner pool starting", "namespace", p.ns, "languages", p.Languages())
	p.replenish()

	replenishEvery := p.cfg.ReplenishInterval
	if replenishEvery <= 0 {
		replenishEvery = 15 * time.Second
	}
	replenishTick := time.NewTicker(replenishEvery)
	defer replenishTick.Stop()
	sweepTick := time.NewTicker(maxDuration(replenishEvery*4, time.Minute))
	defer sweepTick.Stop()

	for {
		select {
		case <-ctx.Done():
			p.drain()
			p.logger.Info("runner pool stopped")
			return
		case <-replenishTick.C:
			p.replenish()
		case <-sweepTick.C:
			p.sweep(ctx)
		}
	}
}

// Claim returns a runner for language: a warm one from the idle pool when
// available, otherwise a cold one created on demand (slower, but the pool
// being empty never fails a deploy). The returned runner is owned by the
// caller until Release. Claim triggers an async replenish to refill the pool.
func (p *Pool) Claim(ctx context.Context, language string) (*Runner, error) {
	if !p.Supports(language) {
		return nil, fmt.Errorf("%w: %q", ErrLanguageUnsupported, language)
	}
	start := time.Now()

	// Warm path: pop an idle runner.
	p.mu.Lock()
	if runners := p.idle[language]; len(runners) > 0 {
		r := runners[len(runners)-1]
		p.idle[language] = runners[:len(runners)-1]
		n := len(p.idle[language])
		p.mu.Unlock()
		p.setIdleGauge(language, n)
		p.recordClaim(language, "warm", start)
		go p.replenish()
		p.logger.Info("runner claimed", "source", "warm", "language", language, "runner", r.Name, "idle_left", n)
		return r, nil
	}
	p.mu.Unlock()

	// Cold path: create one on demand and wait for its agent.
	p.logger.Info("runner pool empty, creating cold runner", "language", language)
	r, err := p.create(ctx, language)
	if err != nil {
		return nil, err
	}
	if err := p.waitReady(ctx, r); err != nil {
		p.untrack(r)
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		p.deleteSandbox(cleanupCtx, r)
		p.recordCreated(language, "failed")
		return nil, fmt.Errorf("cold runner warmup failed: %w", err)
	}
	p.recordCreated(language, "ready")
	p.recordClaim(language, "cold", start)
	go p.replenish()
	p.logger.Info("runner claimed", "source", "cold", "language", language, "runner", r.Name)
	return r, nil
}

// Release recycles a claimed runner: the Sandbox CR is deleted outright so a
// pod that has run user code is never handed to another tenant. The next
// replenish refills the pool.
func (p *Pool) Release(ctx context.Context, r *Runner) {
	if r == nil {
		return
	}
	p.untrack(r)
	p.deleteSandbox(ctx, r)
	p.logger.Info("runner released and recycled", "language", r.Language, "runner", r.Name)
	go p.replenish()
}

// replenish brings each language's idle+pending count up to its configured
// pool size by kicking off background warmups. It never blocks.
func (p *Pool) replenish() {
	p.mu.Lock()
	var spawn []string
	for lang, rc := range p.cfg.Runners {
		have := len(p.idle[lang]) + p.pending[lang]
		for i := have; i < rc.PoolSize; i++ {
			p.pending[lang]++
			spawn = append(spawn, lang)
		}
	}
	p.mu.Unlock()

	for _, lang := range spawn {
		lang := lang
		go func() {
			defer p.decrPending(lang)
			if err := p.warmOne(p.runCtx, lang); err != nil {
				p.logger.Error("runner warmup failed", "language", lang, "error", err)
			}
		}()
	}
}

// warmOne creates one runner Sandbox, waits for its agent to come up, and files
// it into the idle pool. On failure the Sandbox CR is cleaned up.
func (p *Pool) warmOne(ctx context.Context, language string) error {
	r, err := p.create(ctx, language)
	if err != nil {
		p.recordCreated(language, "failed")
		return err
	}
	if err := p.waitReady(ctx, r); err != nil {
		p.untrack(r)
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		p.deleteSandbox(cleanupCtx, r)
		p.recordCreated(language, "failed")
		return err
	}
	p.mu.Lock()
	p.idle[language] = append(p.idle[language], r)
	n := len(p.idle[language])
	p.mu.Unlock()
	p.recordCreated(language, "ready")
	p.setIdleGauge(language, n)
	p.logger.Info("runner warmed", "language", language, "runner", r.Name, "idle", n)
	return nil
}

// create makes one runner Sandbox CR and registers it as tracked. It does not
// wait for readiness.
func (p *Pool) create(ctx context.Context, language string) (*Runner, error) {
	rc, ok := p.cfg.Runners[language]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrLanguageUnsupported, language)
	}
	name := runnerName(language)
	obj := buildSandbox(name, p.ns, language, rc)
	if _, err := p.dyn.Resource(sandboxGVR).Namespace(p.ns).Create(ctx, obj, metav1.CreateOptions{}); err != nil {
		return nil, fmt.Errorf("creating runner Sandbox: %w", err)
	}
	r := &Runner{
		Name:        name,
		Namespace:   p.ns,
		Language:    language,
		Image:       rc.Image,
		CreatedAt:   time.Now(),
		controlPort: rc.ControlPort,
		appPort:     rc.AppPort,
	}
	p.mu.Lock()
	p.tracked[name] = r
	p.mu.Unlock()
	return r, nil
}

// waitReady polls the runner agent's /healthz until it responds or the ready
// timeout elapses.
func (p *Pool) waitReady(ctx context.Context, r *Runner) error {
	timeout := p.cfg.ReadyTimeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		if err := p.probe(ctx, r.ControlURL()); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("runner %s agent not ready after %s: %w", r.Name, timeout, lastErr)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

// sweep recycles idle runners that are too old or whose agent has gone
// unreachable, so the pool self-heals. Runners claimed between the snapshot and
// the eviction pass are left untouched — they are the deployer's to manage.
func (p *Pool) sweep(ctx context.Context) {
	p.mu.Lock()
	var snapshot []*Runner
	for _, runners := range p.idle {
		snapshot = append(snapshot, runners...)
	}
	p.mu.Unlock()
	if len(snapshot) == 0 {
		return
	}

	maxAge := p.cfg.MaxIdleAge
	dead := map[string]bool{}
	for _, r := range snapshot {
		if maxAge > 0 && time.Since(r.CreatedAt) > maxAge {
			dead[r.Name] = true
			continue
		}
		probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := p.probe(probeCtx, r.ControlURL())
		cancel()
		if err != nil {
			p.logger.Warn("idle runner failed health probe, recycling", "runner", r.Name, "error", err)
			dead[r.Name] = true
		}
	}
	if len(dead) == 0 {
		return
	}

	p.mu.Lock()
	var evicted []*Runner
	for lang, runners := range p.idle {
		kept := runners[:0]
		for _, r := range runners {
			if dead[r.Name] {
				evicted = append(evicted, r)
				delete(p.tracked, r.Name)
				continue
			}
			kept = append(kept, r)
		}
		p.idle[lang] = kept
	}
	counts := p.idleCountsLocked()
	p.mu.Unlock()

	for lang, n := range counts {
		p.setIdleGauge(lang, n)
	}
	for _, r := range evicted {
		p.logger.Info("recycling idle runner", "language", r.Language, "runner", r.Name)
		p.deleteSandbox(ctx, r)
	}
	p.replenish()
}

// drain deletes every runner Sandbox the pool still owns, including claimed
// ones — previews are ephemeral, and leaving Sandboxes behind would leak pods.
// Called once on shutdown.
func (p *Pool) drain() {
	p.mu.Lock()
	runners := make([]*Runner, 0, len(p.tracked))
	for _, r := range p.tracked {
		runners = append(runners, r)
	}
	p.tracked = map[string]*Runner{}
	p.idle = map[string][]*Runner{}
	p.pending = map[string]int{}
	p.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, r := range runners {
		p.deleteSandbox(ctx, r)
	}
	p.logger.Info("runner pool drained", "deleted", len(runners))
}

func (p *Pool) deleteSandbox(ctx context.Context, r *Runner) {
	err := p.dyn.Resource(sandboxGVR).Namespace(r.Namespace).Delete(ctx, r.Name, metav1.DeleteOptions{})
	if err != nil && !apierrors.IsNotFound(err) {
		p.logger.Warn("failed to delete runner Sandbox", "runner", r.Name, "error", err)
	}
}

func (p *Pool) untrack(r *Runner) {
	p.mu.Lock()
	delete(p.tracked, r.Name)
	p.mu.Unlock()
}

func (p *Pool) decrPending(language string) {
	p.mu.Lock()
	if p.pending[language] > 0 {
		p.pending[language]--
	}
	p.mu.Unlock()
}

func (p *Pool) idleCountsLocked() map[string]int {
	out := make(map[string]int, len(p.idle))
	for lang, runners := range p.idle {
		out[lang] = len(runners)
	}
	return out
}

func (p *Pool) recordClaim(language, source string, start time.Time) {
	if p.m == nil {
		return
	}
	p.m.PoolClaimsTotal.WithLabelValues(language, source).Inc()
	p.m.PoolClaimDuration.WithLabelValues(language, source).Observe(time.Since(start).Seconds())
}

func (p *Pool) recordCreated(language, status string) {
	if p.m == nil {
		return
	}
	p.m.PoolRunnersCreated.WithLabelValues(language, status).Inc()
}

func (p *Pool) setIdleGauge(language string, n int) {
	if p.m == nil {
		return
	}
	p.m.PoolRunnersIdle.WithLabelValues(language).Set(float64(n))
}

// runnerName returns a DNS-safe unique name for a runner Sandbox.
func runnerName(language string) string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return fmt.Sprintf("vibed-runner-%s-%s", language, hex.EncodeToString(b))
}

func maxDuration(a, b time.Duration) time.Duration {
	if a > b {
		return a
	}
	return b
}
