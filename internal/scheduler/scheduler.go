// Package scheduler runs due checks concurrently and performs periodic
// database maintenance (retention purge).
package scheduler

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/brainslad/monitoriq/internal/checks"
	"github.com/brainslad/monitoriq/internal/store"
)

type Scheduler struct {
	Store       *store.Store
	Concurrency int
	Log         *slog.Logger

	mu       sync.Mutex
	inflight map[int64]bool
}

func New(st *store.Store, concurrency int, log *slog.Logger) *Scheduler {
	return &Scheduler{Store: st, Concurrency: concurrency, Log: log, inflight: map[int64]bool{}}
}

// Run blocks until ctx is cancelled.
func (s *Scheduler) Run(ctx context.Context) {
	sem := make(chan struct{}, s.Concurrency)
	var wg sync.WaitGroup
	tick := time.NewTicker(time.Second)
	defer tick.Stop()

	go s.maintenanceLoop(ctx)

	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			return
		case <-tick.C:
		}
		due, err := s.Store.DueMonitors(ctx)
		if err != nil {
			if ctx.Err() == nil {
				s.Log.Error("erro ao obter monitores", "err", err)
			}
			continue
		}
		for i := range due {
			m := due[i]
			s.mu.Lock()
			if s.inflight[m.ID] {
				s.mu.Unlock()
				continue
			}
			s.inflight[m.ID] = true
			s.mu.Unlock()

			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				s.release(m.ID)
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-sem }()
				defer s.release(m.ID)
				s.runOne(ctx, &m)
			}()
		}
	}
}

func (s *Scheduler) release(id int64) {
	s.mu.Lock()
	delete(s.inflight, id)
	s.mu.Unlock()
}

func (s *Scheduler) runOne(ctx context.Context, m *store.Monitor) {
	at := time.Now()
	res := checks.Run(ctx, m.CheckSpec())
	if ctx.Err() != nil {
		return
	}
	// Use a fresh context so a result is saved even during shutdown.
	sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tr, err := s.Store.RecordResult(sctx, m, res, at)
	if err != nil {
		s.Log.Error("erro ao gravar resultado", "monitor", m.ID, "err", err)
		return
	}
	if tr != nil {
		s.Log.Info("mudança de estado", "monitor", m.Name, "id", m.ID,
			"de", tr.From.String(), "para", tr.To.String(), "mensagem", tr.Message)
	}
}

func (s *Scheduler) maintenanceLoop(ctx context.Context) {
	run := func() {
		st, err := s.Store.Purge(ctx)
		if err != nil {
			if ctx.Err() == nil {
				s.Log.Error("erro na limpeza de retenção", "err", err)
			}
			return
		}
		if st.Results+st.Hourly+st.Events > 0 {
			s.Log.Info("limpeza de retenção", "resultados", st.Results, "agregados", st.Hourly, "eventos", st.Events)
		}
	}
	select {
	case <-ctx.Done():
		return
	case <-time.After(30 * time.Second):
	}
	run()
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			run()
		}
	}
}
