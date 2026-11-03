package notify

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"
)

// Timing holds every delay and bound the Service uses. Zero fields take the
// defaults noted on each field.
type Timing struct {
	Spacing       time.Duration   // default 3s: gap after each sent message
	Backoff       []time.Duration // default 2s, 4s (len = retries; 3 attempts in total)
	MaxRetryAfter time.Duration   // default 10s: cap on an honoured 429 retry_after
	MessageBudget time.Duration   // default 30s: all attempts and waits for one message
	ErrorWindow   time.Duration   // default 5m
	CrashWindow   time.Duration   // default 1h
	TickEvery     time.Duration   // default 30s
	StopDrain     time.Duration   // default 5s
	QueueSize     int             // default 32 (batch lane)
	UrgentSize    int             // default 8 (crash lane, drained first)
}

// defaultTiming is the production Timing.
func defaultTiming() Timing {
	return Timing{
		Spacing:       3 * time.Second,
		Backoff:       []time.Duration{2 * time.Second, 4 * time.Second},
		MaxRetryAfter: 10 * time.Second,
		MessageBudget: 30 * time.Second,
		ErrorWindow:   5 * time.Minute,
		CrashWindow:   time.Hour,
		TickEvery:     30 * time.Second,
		StopDrain:     5 * time.Second,
		QueueSize:     32,
		UrgentSize:    8,
	}
}

// withDefaults returns t with every zero field taken from defaultTiming.
func (t Timing) withDefaults() Timing {
	d := defaultTiming()
	fill := func(v *time.Duration, def time.Duration) {
		if *v == 0 {
			*v = def
		}
	}
	fill(&t.Spacing, d.Spacing)
	fill(&t.MaxRetryAfter, d.MaxRetryAfter)
	fill(&t.MessageBudget, d.MessageBudget)
	fill(&t.ErrorWindow, d.ErrorWindow)
	fill(&t.CrashWindow, d.CrashWindow)
	fill(&t.TickEvery, d.TickEvery)
	fill(&t.StopDrain, d.StopDrain)
	if len(t.Backoff) == 0 {
		t.Backoff = d.Backoff
	}
	if t.QueueSize == 0 {
		t.QueueSize = d.QueueSize
	}
	if t.UrgentSize == 0 {
		t.UrgentSize = d.UrgentSize
	}

	return t
}

// Option configures a Service.
type Option func(*Service)

// WithPublicURL sets the instance URL that opens every message ("Folio" when
// empty).
func WithPublicURL(u string) Option { return func(s *Service) { s.publicURL = u } }

// WithClock replaces time.Now for the timestamps the Service records.
func WithClock(now func() time.Time) Option { return func(s *Service) { s.now = now } }

// WithTiming overrides the default Timing; zero fields keep their defaults.
func WithTiming(t Timing) Option { return func(s *Service) { s.timing = t } }

// Service delivers notifications to Telegram through a bounded two-lane queue
// drained by a single sender goroutine.
type Service struct {
	log    *slog.Logger // base logger, never the error-tapped one
	store  *Store
	client *Client
	cfg    atomic.Pointer[Config]
	cfgMu  sync.Mutex // serialises UpdateTelegram
	status statusBox
	now    func() time.Time
	timing Timing

	publicURL string
	prefix    string

	urgent chan message // KindCrash only, drained first
	batch  chan message // every other kind

	crashMu sync.Mutex             // guards crashes: ReportCrash runs on any goroutine, tick on the ticker
	crashes map[string]*crashEntry // open crash de-duplication windows, at most maxCrashEntries

	failingMu sync.Mutex     // guards failing: runs finish on any goroutine
	failing   map[int64]bool // library IDs whose last run failed

	dropMu  sync.Mutex
	dropped int // messages dropped on a full lane since the last success

	closed  atomic.Bool   // intake closed by Stop
	closing chan struct{} // closed by Stop: the sender exits once the lanes are empty

	cancel     context.CancelFunc // lifecycle: cancelled when Stop gives up draining
	stopTicker context.CancelFunc

	lifeMu     sync.Mutex
	started    bool
	stopped    bool
	senderDone chan struct{}
	tickerDone chan struct{}
}

// New builds a Service and loads the stored Config once. It fails only when
// the settings cannot be read.
func New(log *slog.Logger, store *Store, client *Client, opts ...Option) (*Service, error) {
	s := &Service{
		log:     log,
		store:   store,
		client:  client,
		now:     time.Now,
		closing: make(chan struct{}),
	}
	for _, opt := range opts {
		opt(s)
	}
	s.timing = s.timing.withDefaults()
	s.prefix = prefix(s.publicURL)
	s.urgent = make(chan message, s.timing.UrgentSize)
	s.batch = make(chan message, s.timing.QueueSize)

	cfg, err := store.Load(context.Background())
	if err != nil {
		return nil, fmt.Errorf("load telegram settings: %w", err)
	}
	s.cfg.Store(&cfg)

	return s, nil
}

// Start launches the sender and the ticker. It does nothing once the Service
// has been started or stopped.
func (s *Service) Start() {
	s.lifeMu.Lock()
	defer s.lifeMu.Unlock()
	if s.started || s.stopped {
		return
	}
	s.started = true

	ctx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.senderDone = make(chan struct{})
	go func() {
		defer close(s.senderDone)
		s.sender(ctx)
	}()

	tickCtx, stopTicker := context.WithCancel(ctx)
	s.stopTicker = stopTicker
	s.tickerDone = make(chan struct{})
	go func() {
		defer close(s.tickerDone)
		s.tickLoop(tickCtx)
	}()
}

// Stop stops the ticker, flushes open windows into the queue, closes intake and
// gives the sender up to StopDrain to empty the lanes; whatever is left after
// that is dropped. It is idempotent.
func (s *Service) Stop() {
	s.lifeMu.Lock()
	if s.stopped {
		s.lifeMu.Unlock()
		return
	}
	s.stopped = true
	started := s.started
	s.lifeMu.Unlock()

	if started {
		s.stopTicker()
		<-s.tickerDone
	}
	s.flush(s.now())
	s.closed.Store(true)
	close(s.closing)

	if !started {
		return
	}
	drain := time.NewTimer(s.timing.StopDrain)
	select {
	case <-s.senderDone:
	case <-drain.C:
	}
	drain.Stop()
	s.cancel()
	<-s.senderDone
}

// TelegramSettings returns the redacted settings and the delivery status.
func (s *Service) TelegramSettings(context.Context) (Settings, error) {
	return view(*s.cfg.Load(), s.status.snapshot()), nil
}

// UpdateTelegram validates u, persists the result and makes it the active
// Config. On any error nothing changes. Switching off (or an unready result)
// discards the collected state, so nothing accumulates while off.
func (s *Service) UpdateTelegram(ctx context.Context, u Update) (Settings, error) {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()

	next, err := validate(u, *s.cfg.Load())
	if err != nil {
		return Settings{}, err
	}
	if err := s.store.Save(ctx, next); err != nil {
		return Settings{}, fmt.Errorf("save telegram settings: %w", err)
	}
	s.cfg.Store(&next)
	if !next.Enabled || !next.Ready() {
		s.resetCollected()
	}

	return view(next, s.status.snapshot()), nil
}

// SendTest sends one test message synchronously with the saved Config, whether
// or not notifications are enabled. It makes a single attempt, bypasses the
// queue and records the outcome in the delivery status.
func (s *Service) SendTest(ctx context.Context) error {
	cfg := *s.cfg.Load()
	if !cfg.Ready() {
		return ErrNotConfigured
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	if err := s.client.Send(ctx, cfg, testText(s.prefix)); err != nil {
		s.status.recordFailure(s.now(), err.Error())

		return err
	}
	s.status.recordSuccess(s.now())

	return nil
}

// DeliveryStatus returns a copy of the most recent delivery outcomes.
func (s *Service) DeliveryStatus() DeliveryStatus { return s.status.snapshot() }

func (s *Service) tickLoop(ctx context.Context) {
	t := time.NewTicker(s.timing.TickEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.tick(s.now())
		}
	}
}

// tick closes the time windows that have expired at now.
func (s *Service) tick(now time.Time) {
	s.tickCrashes(now)
}

// flush closes every open window into the queue on Stop. Empty in this phase.
func (s *Service) flush(time.Time) {}

// resetCollected discards all in-memory collected state when notifications
// are switched off; UpdateTelegram calls it.
func (s *Service) resetCollected() {
	s.resetCrashes()
	s.resetFailing()
}
