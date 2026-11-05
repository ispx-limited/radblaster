package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/ispx-limited/radblaster/internal/api"
	"github.com/ispx-limited/radblaster/internal/profile"
	"github.com/ispx-limited/radblaster/internal/web"
)

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	var (
		addr     = fs.String("addr", "127.0.0.1:8080", "address to serve the interface on")
		profiles = fs.String("profiles", "profiles", "directory to keep saved profiles in")
		noAuto   = fs.Bool("no-auto-dictionary", false, "do not look for dictionaries in the usual system locations")
		dicts    stringList
		hosts    stringList
	)
	fs.Var(&dicts, "dictionary", "dictionary file or directory to load; repeatable")
	fs.Var(&hosts, "allowed-host", "hostname this instance may be addressed by, for use behind a proxy; repeatable")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Serve the web interface.\n\nFlags:")
		fs.PrintDefaults()
		fmt.Fprintln(os.Stderr, "\nThe interface binds to localhost by default. It can start runs against")
		fmt.Fprintln(os.Stderr, "any reachable server and has no authentication of its own, so exposing")
		fmt.Fprintln(os.Stderr, "it on a shared network hands that ability to anyone who can reach it.")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	dict, dictPaths, err := loadDictionaries(dicts, *noAuto)
	if err != nil {
		return err
	}

	store, err := profile.NewStore(*profiles)
	if err != nil {
		return err
	}

	srv := api.New(dict, web.FS(), store)
	srv.SetDictionaryPaths(dictPaths)
	for _, h := range hosts {
		srv.AllowHost(h)
	}
	defer srv.Close()

	httpSrv := &http.Server{
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		// A body that arrives one byte at a time would otherwise hold its
		// connection, and its goroutine, for as long as the sender cared to
		// keep it. There is no write deadline: the event stream is meant to
		// stay open for the length of a run, which can be hours.
		ReadTimeout: 30 * time.Second,
		IdleTimeout: 120 * time.Second,
	}

	reportDictionaries(dict, dictPaths)

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", *addr, err)
	}

	fmt.Fprintf(os.Stderr, "radblaster is at http://%s\n", ln.Addr())
	fmt.Fprintf(os.Stderr, "Profiles are kept in %s\n", store.Dir())
	// Warn on anything that is not loopback, taken from the listener rather
	// than the flag. Pinning a LAN address is exactly what someone does when
	// they want a colleague to see the dashboard, and that used to pass without
	// a word because only the wildcard forms were checked.
	if !isLoopbackAddr(ln.Addr()) {
		fmt.Fprintln(os.Stderr, "Warning: this is reachable from the network and has no authentication.")
		fmt.Fprintln(os.Stderr, "Anyone who can reach it can start a run against any server this machine can.")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	errc := make(chan error, 1)
	go func() { errc <- httpSrv.Serve(ln) }()

	select {
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return httpSrv.Shutdown(shutdown)
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

// isLoopbackAddr reports whether a listening address is confined to this
// machine. A wildcard bind is not, even though it includes loopback.
func isLoopbackAddr(a net.Addr) bool {
	t, ok := a.(*net.TCPAddr)
	if !ok {
		return false
	}
	return t.IP != nil && t.IP.IsLoopback()
}
