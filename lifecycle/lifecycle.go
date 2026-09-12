package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

const defaultStopTimeout = 15 * time.Second

// Ref identifies a registered component. Add returns one; After consumes one.
//
// An edge can only reference an already-declared component, which is what makes
// a dependency cycle unrepresentable rather than merely detected. It carries
// its owner so a Ref from a different Lifecycle is caught rather than silently
// resolving to whatever component happens to share its index.
type Ref struct {
	owner *Lifecycle
	i     int
}

// Option configures a single component.
type Option func(*component)

// After declares that this component starts only once every ref has started,
// and stops before they do.
func After(refs ...Ref) Option {
	return func(c *component) { c.afterRefs = append(c.afterRefs, refs...) }
}

// StopTimeout overrides Config.StopTimeout for this component.
func StopTimeout(d time.Duration) Option {
	return func(c *component) { c.stopTimeout = d }
}

// Config configures a Lifecycle. Its zero value is usable.
type Config struct {
	// DrainDelay is how long to wait after the OnDrain hooks run and before
	// any component is stopped. Zero means no delay.
	DrainDelay time.Duration

	// StopTimeout bounds each component's Stop. Zero means 15s.
	StopTimeout time.Duration

	// Logger receives start, stop, and drain events. Nil means silent.
	//
	// This is a stdlib *slog.Logger, not svcrt/logging: accepting one adds no
	// dependency, and a caller passes whatever logger they already have.
	// lifecycle never reaches for slog.Default().
	Logger *slog.Logger
}

// Lifecycle sequences components through startup and shutdown.
type Lifecycle struct {
	cfg   Config
	comps []component
	errs  []error // construction errors, reported by Run

	drainHooks []func()

	fatalOnce sync.Once
	fatalCh   chan struct{}
	fatalErr  error
}

// New returns a Lifecycle. Zero-valued Config fields take their defaults.
func New(cfg Config) *Lifecycle {
	if cfg.StopTimeout == 0 {
		cfg.StopTimeout = defaultStopTimeout
	}
	return &Lifecycle{cfg: cfg, fatalCh: make(chan struct{})}
}

// Add registers a component and returns a Ref other components can depend on.
//
// Declaration order is dependency order: After can only name a component
// already added, which is what makes a cycle unrepresentable.
func (l *Lifecycle) Add(name string, start StartFunc, stop StopFunc, opts ...Option) Ref {
	c := component{name: name, start: start, stop: stop}
	for _, o := range opts {
		o(&c)
	}
	// Resolve each declared Ref to an index, rejecting any that does not
	// belong to this Lifecycle or that names a component this Lifecycle
	// hasn't added yet. Checking the owner rather than only the index bound
	// matters: a foreign Ref whose index happens to be in range would
	// otherwise resolve silently to an unrelated component. Checking the
	// bound too matters just as much: without it, a same-owner Ref whose
	// index is not yet populated (in particular one naming the component
	// being added right now) would resolve to a zero index instead of being
	// rejected, which is exactly the silent forward-reference failure that
	// makes levels() schedule a component alongside its own dependency.
	for _, r := range c.afterRefs {
		if r.owner != l || r.i < 0 || r.i >= len(l.comps) {
			l.errs = append(l.errs, fmt.Errorf("lifecycle: %s: dependency Ref belongs to a different Lifecycle or is out of range for it", name))
			continue
		}
		c.after = append(c.after, r.i)
	}
	l.comps = append(l.comps, c)
	return Ref{owner: l, i: len(l.comps) - 1}
}

// OnDrain registers a hook run once, before any component is stopped.
func (l *Lifecycle) OnDrain(f func()) { l.drainHooks = append(l.drainHooks, f) }

func (l *Lifecycle) log() *slog.Logger { return l.cfg.Logger }

func (l *Lifecycle) logf(level slog.Level, msg string, args ...any) {
	if l.cfg.Logger != nil {
		l.cfg.Logger.Log(context.Background(), level, msg, args...)
	}
}

// Run starts every component, waits for ctx, then drains.
func (l *Lifecycle) Run(ctx context.Context) error {
	if len(l.errs) > 0 {
		return errors.Join(l.errs...)
	}

	lv := levels(l.comps)
	started := make([]bool, len(l.comps))

	for _, level := range lv {
		if err := l.startLevel(ctx, level, started); err != nil {
			// A component whose Start already succeeded may still fail to
			// Stop during the unwind. That failure is just as much a part
			// of "join every error" as the Start failures are, so it must
			// not be dropped on the floor here.
			stopErr := l.stopStarted(ctx, lv, started)
			return errors.Join(err, stopErr)
		}
	}

	<-ctx.Done()

	l.drain()
	return l.stopStarted(ctx, lv, started)
}

// startLevel starts every component in level concurrently.
//
// On the first failure the level's context is cancelled and every in-flight
// Start is WAITED FOR. Waiting rather than returning immediately is what keeps
// the unwind correct: an abandoned Start may have bound a listener or opened a
// pool that nothing is tracking, and Stop is never called for a Start that
// never returned. Cancelling rather than waiting for natural completion is what
// keeps a doomed boot from hanging for the duration of its slowest component.
func (l *Lifecycle) startLevel(ctx context.Context, level []int, started []bool) error {
	lctx, cancel := context.WithCancel(ctx)
	defer cancel()

	var wg sync.WaitGroup
	errs := make([]error, len(level))
	for n, i := range level {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l.logf(slog.LevelInfo, "starting component", "component", l.comps[i].name)
			if err := l.comps[i].start(lctx); err != nil {
				errs[n] = fmt.Errorf("lifecycle: %s: start: %w", l.comps[i].name, err)
				cancel() // tell the siblings to give up
				return
			}
			started[i] = true
		}()
	}
	wg.Wait()
	return errors.Join(errs...)
}

// drain runs the OnDrain hooks and waits DrainDelay.
func (l *Lifecycle) drain() {
	for _, h := range l.drainHooks {
		h()
	}
	if l.cfg.DrainDelay > 0 {
		l.logf(slog.LevelInfo, "draining", "delay", l.cfg.DrainDelay)
		time.Sleep(l.cfg.DrainDelay)
	}
}

// stopStarted stops every started component in reverse level order.
func (l *Lifecycle) stopStarted(ctx context.Context, lv [][]int, started []bool) error {
	var errs []error
	var mu sync.Mutex

	for n := len(lv) - 1; n >= 0; n-- {
		var wg sync.WaitGroup
		for _, i := range lv[n] {
			if !started[i] {
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				if err := l.stopOne(ctx, i); err != nil {
					mu.Lock()
					errs = append(errs, err)
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
	}
	return errors.Join(errs...)
}

func (l *Lifecycle) stopOne(ctx context.Context, i int) error {
	c := l.comps[i]
	d := c.stopTimeout
	if d == 0 {
		d = l.cfg.StopTimeout
	}

	// WithoutCancel, not Background: by the time we drain, ctx is already
	// cancelled, so a derived context would be cancelled at birth and every
	// Shutdown would return instantly having drained nothing -- which looks
	// like a fast clean shutdown and is actually data loss. WithoutCancel
	// sheds the cancellation while keeping the caller's values.
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), d)
	defer cancel()

	l.logf(slog.LevelInfo, "stopping component", "component", c.name)
	if err := c.stop(sctx); err != nil {
		return fmt.Errorf("lifecycle: %s: stop: %w", c.name, err)
	}
	return nil
}
