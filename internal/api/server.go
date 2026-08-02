package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ispx-limited/radblaster/internal/engine"
	"github.com/ispx-limited/radblaster/internal/profile"
	"github.com/ispx-limited/radblaster/internal/radius"
)

// sampleInterval is how often a run is sampled and pushed to anyone watching.
// Fast enough that a server starting to struggle is visible as it happens,
// slow enough that a chart stays legible and the connection stays cheap.
const sampleInterval = 500 * time.Millisecond

// maxSamples bounds the series a run keeps, so a soak measured in hours cannot
// grow without limit. At the sampling interval this is a little over ten
// minutes of detail.
const maxSamples = 1200

// Server owns the run the interface is driving.
//
// One run at a time is deliberate. Two runs from one process would share the
// identifier space and the source ports, so their results would interfere in
// ways that are invisible in the numbers.
type Server struct {
	dict      *radius.Dictionary
	dictPaths []string
	ui        fs.FS
	profiles  *profile.Store

	// allowedHosts are the extra names this instance answers to, beyond an
	// address literal and localhost. See checkHost.
	allowedHosts map[string]bool

	mu      sync.Mutex
	state   State
	cfg     RunConfig
	runner  *engine.Runner
	result  *engine.Result
	err     string
	started time.Time
	history []Sample
	// latest is the most recent sample of a running run. Rates are deltas
	// since the previous snapshot, so exactly one goroutine may take them:
	// letting each watcher sample independently would split the deltas
	// between them and every reader would see a fraction of the truth.
	latest *Status

	// watchers are the connected streams, each with a buffered channel so a
	// slow browser cannot hold up the run or anyone else watching it.
	watchers map[chan Status]struct{}
}

// New returns a server. ui may be nil, in which case only the data endpoints
// are served, and profiles may be nil to run without a profile store.
func New(dict *radius.Dictionary, ui fs.FS, profiles *profile.Store) *Server {
	if dict == nil {
		dict = radius.StandardDictionary()
	}
	return &Server{
		dict:         dict,
		ui:           ui,
		profiles:     profiles,
		state:        StateIdle,
		watchers:     make(map[chan Status]struct{}),
		allowedHosts: make(map[string]bool),
	}
}

// AllowHost adds a name this instance will answer to. Only needed when
// something in front of it, such as a reverse proxy, addresses it by name
// rather than by address; see checkHost for why a name is not accepted
// otherwise.
func (s *Server) AllowHost(host string) {
	if host = strings.ToLower(strings.TrimSpace(host)); host != "" {
		s.allowedHosts[host] = true
	}
}

// SetDictionaryPaths records where the loaded dictionaries came from, so the
// interface can report them and a saved profile can pin them.
func (s *Server) SetDictionaryPaths(paths []string) { s.dictPaths = paths }

// Handler returns the routes the interface is served on.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/run", s.startRun)
	mux.HandleFunc("DELETE /api/run", s.stopRun)
	mux.HandleFunc("GET /api/status", s.getStatus)
	mux.HandleFunc("GET /api/events", s.stream)
	mux.HandleFunc("GET /api/dictionary", s.dictionaryInfo)
	mux.HandleFunc("GET /api/attributes", s.searchAttributes)
	mux.HandleFunc("GET /api/profiles", s.listProfiles)
	mux.HandleFunc("GET /api/profiles/{name}", s.getProfile)
	mux.HandleFunc("PUT /api/profiles/{name}", s.putProfile)
	mux.HandleFunc("DELETE /api/profiles/{name}", s.deleteProfile)

	if s.ui != nil {
		mux.Handle("/", http.FileServer(http.FS(s.ui)))
	}
	return guard(mux, s.allowedHosts)
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
	s.history = nil
	s.latest = nil
	s.mu.Unlock()

	go s.sample(runner)
	go s.watch(runner)

	writeJSON(w, http.StatusAccepted, s.status())
}

// sample is the single reader of the run's snapshots. It records the series
// and tells everyone watching.
func (s *Server) sample(runner *engine.Runner) {
	ticker := time.NewTicker(sampleInterval)
	defer ticker.Stop()

	for {
		select {
		case <-runner.Done():
			return
		case <-ticker.C:
			snap := runner.Snapshot()
			st := s.statusFromSnapshot(snap)

			s.mu.Lock()
			if s.runner != runner {
				s.mu.Unlock()
				return
			}
			auth := snap.Access()
			s.history = append(s.history, Sample{
				T:        snap.Elapsed.Milliseconds(),
				Offered:  snap.SetupRate,
				Answered: snap.AnswerRate,
				Lost:     snap.LossRate,
				P95:      auth.Latency.Quantile(0.95),
				Service:  auth.ServiceTime.Quantile(0.95),
			})
			if len(s.history) > maxSamples {
				s.history = s.history[len(s.history)-maxSamples:]
			}
			st.History = append([]Sample(nil), s.history...)
			s.latest = &st
			s.mu.Unlock()

			s.broadcast(st)
		}
	}
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

// statusFromSnapshot renders a live snapshot, without the history, which the
// caller attaches under the lock.
func (s *Server) statusFromSnapshot(snap engine.Snapshot) Status {
	s.mu.Lock()
	st := Status{State: s.state, Config: s.cfg, Error: s.err}
	if !s.started.IsZero() {
		st.StartedAt = s.started.UTC().Format(time.RFC3339)
	}
	s.mu.Unlock()

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
	return st
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

	// Idle runs still get a heartbeat so a proxy in the path does not decide
	// the connection has gone quiet and close it.
	ticker := time.NewTicker(15 * time.Second)
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
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return
			}
			flusher.Flush()
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
	state, cfg, result, errText, started := s.state, s.cfg, s.result, s.err, s.started
	latest, history := s.latest, append([]Sample(nil), s.history...)
	s.mu.Unlock()

	// A running run is reported from the sampler's most recent reading rather
	// than by taking another, so every reader sees the same numbers and the
	// rate deltas are not split between them.
	if state == StateRunning && latest != nil {
		return *latest
	}

	st := Status{State: state, Config: cfg, Error: errText, History: history}
	if !started.IsZero() {
		st.StartedAt = started.UTC().Format(time.RFC3339)
	}

	switch {
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

// noProfiles reports that the server was started without a store, so a caller
// is told why rather than being given an empty list that looks like none saved.
func (s *Server) noProfiles(w http.ResponseWriter) bool {
	if s.profiles != nil {
		return false
	}
	writeError(w, http.StatusNotImplemented, errors.New("this instance was started without a profile directory"))
	return true
}

func (s *Server) listProfiles(w http.ResponseWriter, _ *http.Request) {
	if s.noProfiles(w) {
		return
	}
	names, err := s.profiles.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	if names == nil {
		names = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"profiles": names, "dir": s.profiles.Dir()})
}

func (s *Server) getProfile(w http.ResponseWriter, r *http.Request) {
	if s.noProfiles(w) {
		return
	}
	p, err := s.profiles.Get(r.PathValue("name"))
	if err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, profile.ErrNotFound) {
			code = http.StatusNotFound
		}
		writeError(w, code, err)
		return
	}
	// Returned as run settings so the interface can load it straight into the
	// form, with the credentials left empty for the operator to supply.
	writeJSON(w, http.StatusOK, map[string]any{"profile": p, "config": FromProfile(p)})
}

func (s *Server) putProfile(w http.ResponseWriter, r *http.Request) {
	if s.noProfiles(w) {
		return
	}
	var cfg RunConfig
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&cfg); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("could not read the profile: %w", err))
		return
	}

	p := cfg.ToProfile(r.PathValue("name"))
	if err := s.profiles.Put(p); err != nil {
		writeError(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"profile": p})
}

func (s *Server) deleteProfile(w http.ResponseWriter, r *http.Request) {
	if s.noProfiles(w) {
		return
	}
	if err := s.profiles.Delete(r.PathValue("name")); err != nil {
		code := http.StatusBadRequest
		if errors.Is(err, profile.ErrNotFound) {
			code = http.StatusNotFound
		}
		writeError(w, code, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// dictionaryInfo reports what names are resolvable and where they came from.
// Discovery is convenient, but a run that has to be repeatable should not
// depend on what happened to be installed, so the paths are always visible and
// are written into a profile when one is saved.
func (s *Server) dictionaryInfo(w http.ResponseWriter, _ *http.Request) {
	attrs, vendors := s.dict.Count()
	paths := s.dictPaths
	if paths == nil {
		paths = []string{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"paths":      paths,
		"attributes": attrs,
		"vendors":    vendors,
	})
}

// searchAttributes backs the name suggestions in the interface.
func (s *Server) searchAttributes(w http.ResponseWriter, r *http.Request) {
	limit := 40
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"matches": s.dict.Search(r.URL.Query().Get("q"), limit),
	})
}
