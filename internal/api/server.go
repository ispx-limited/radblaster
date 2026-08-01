package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"sync"
	"time"

	"github.com/ispx-limited/radblaster/internal/engine"
	"github.com/ispx-limited/radblaster/internal/radius"
)

// streamInterval is how often a watching browser is sent an update. Fast
// enough that a server starting to struggle is visible as it happens, slow
// enough that a chart stays legible and the connection stays cheap.
const streamInterval = 500 * time.Millisecond

// Server owns the run the interface is driving.
//
// One run at a time is deliberate. Two runs from one process would share the
// identifier space and the source ports, so their results would interfere in
// ways that are invisible in the numbers.
type Server struct {
	dict *radius.Dictionary
	ui   fs.FS

	mu      sync.Mutex
	state   State
	cfg     RunConfig
	runner  *engine.Runner
	result  *engine.Result
	err     string
	started time.Time

	// watchers are the connected streams, each with a buffered channel so a
	// slow browser cannot hold up the run or anyone else watching it.
	watchers map[chan Status]struct{}
}

// New returns a server. ui may be nil, in which case only the interface's data
// endpoints are served.
func New(dict *radius.Dictionary, ui fs.FS) *Server {
	if dict == nil {
		dict = radius.StandardDictionary()
	}
	return &Server{
		dict:     dict,
		ui:       ui,
		state:    StateIdle,
		watchers: make(map[chan Status]struct{}),
	}
}

// Handler returns the routes the interface is served on.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/run", s.startRun)
	mux.HandleFunc("DELETE /api/run", s.stopRun)
	mux.HandleFunc("GET /api/status", s.getStatus)
	mux.HandleFunc("GET /api/events", s.stream)

	if s.ui != nil {
		mux.Handle("/", http.FileServer(http.FS(s.ui)))
	}
	return mux
}

// Close stops any run still going and releases its sockets.
func (s *Server) Close() {
	s.mu.Lock()
	r := s.runner
	s.mu.Unlock()

	if r != nil {
		r.Stop()
		_, _ = r.Wait()
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, code int, err error) {
	writeJSON(w, code, map[string]string{"error": err.Error()})
}

func (s *Server) startRun(w http.ResponseWriter, r *http.Request) {
	var cfg RunConfig
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&cfg); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("could not read the run settings: %w", err))
		return
	}

	engineCfg, err := cfg.toEngine(s.dict)
	if err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}

	s.mu.Lock()
	if s.state == StateRunning {
		s.mu.Unlock()
		writeError(w, http.StatusConflict, errors.New("a run is already going; stop it before starting another"))
		return
	}

	runner, err := engine.Start(context.Background(), engineCfg)
	if err != nil {
		s.state, s.err = StateFailed, err.Error()
		s.mu.Unlock()
		writeError(w, http.StatusBadRequest, err)
		return
	}

	s.state = StateRunning
	s.cfg = cfg.redacted()
	s.runner = runner
	s.result = nil
	s.err = ""
	s.started = time.Now()
	s.mu.Unlock()

	go s.watch(runner)

	writeJSON(w, http.StatusAccepted, s.status())
}

// watch collects the result once a run ends and tells everyone watching.
func (s *Server) watch(runner *engine.Runner) {
	<-runner.Done()
	res, err := runner.Wait()

	s.mu.Lock()
	if s.runner == runner {
		s.result = res
		s.state = StateDone
		if err != nil {
			s.state, s.err = StateFailed, err.Error()
		}
	}
	s.mu.Unlock()

	s.broadcast(s.status())
}

func (s *Server) stopRun(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	runner := s.runner
	running := s.state == StateRunning
	s.mu.Unlock()

	if !running || runner == nil {
		writeError(w, http.StatusConflict, errors.New("no run is going"))
		return
	}
	runner.Stop()
	writeJSON(w, http.StatusAccepted, s.status())
}

func (s *Server) getStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.status())
}

// stream pushes the status to a browser as server-sent events.
//
// Sending the current state on connect means a browser that arrives partway
// through a run, or reconnects after a drop, sees the whole picture rather
// than only what changed afterwards.
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, errors.New("streaming is not supported here"))
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	ch := s.addWatcher()
	defer s.removeWatcher(ch)

	send := func(st Status) bool {
		b, err := json.Marshal(st)
		if err != nil {
			return false
		}
		if _, err := fmt.Fprintf(w, "data: %s\n\n", b); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	if !send(s.status()) {
		return
	}

	ticker := time.NewTicker(streamInterval)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case st := <-ch:
			if !send(st) {
				return
			}
		case <-ticker.C:
			if !send(s.status()) {
				return
			}
		}
	}
}

func (s *Server) addWatcher() chan Status {
	ch := make(chan Status, 4)
	s.mu.Lock()
	s.watchers[ch] = struct{}{}
	s.mu.Unlock()
	return ch
}

func (s *Server) removeWatcher(ch chan Status) {
	s.mu.Lock()
	delete(s.watchers, ch)
	s.mu.Unlock()
}

// broadcast tells every watcher without waiting for any of them. A browser
// that has stopped reading is skipped rather than allowed to block the run.
func (s *Server) broadcast(st Status) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for ch := range s.watchers {
		select {
		case ch <- st:
		default:
		}
	}
}

// status assembles the current picture, from the live run if one is going and
// from the finished result otherwise.
func (s *Server) status() Status {
	s.mu.Lock()
	state, cfg, runner, result, errText, started := s.state, s.cfg, s.runner, s.result, s.err, s.started
	s.mu.Unlock()

	st := Status{State: state, Config: cfg, Error: errText}
	if !started.IsZero() {
		st.StartedAt = started.UTC().Format(time.RFC3339)
	}

	switch {
	case state == StateRunning && runner != nil:
		snap := runner.Snapshot()
		st.ElapsedMS = snap.Elapsed.Milliseconds()
		for _, m := range snap.Exchanges {
			st.Exchanges = append(st.Exchanges, exchangeOf(m))
		}
		st.DynAuth = dynAuthOf(snap.DynAuth)
		st.SessionsStarted = snap.SessionsStarted
		st.SessionsCompleted = snap.SessionsCompleted
		st.SessionsOnline = snap.SessionsOnline
		st.Outstanding = snap.Outstanding
		st.SetupRate = snap.SetupRate
		st.AnswerRate = snap.AnswerRate
		st.LossRate = snap.LossRate

	case result != nil:
		st.ElapsedMS = result.Elapsed.Milliseconds()
		for _, m := range result.Exchanges {
			st.Exchanges = append(st.Exchanges, exchangeOf(m))
		}
		st.DynAuth = dynAuthOf(result.DynAuth)
		st.SessionsStarted = result.SessionsStarted
		st.SessionsCompleted = result.SessionsCompleted
		st.OfferedRate = result.OfferedRate
		st.AchievedRate = result.AchievedRate
		st.Links = result.Links
		st.Notes = result.Notes()
	}

	return st
}
