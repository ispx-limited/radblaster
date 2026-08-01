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
	"github.com/ispx-limited/radblaster/internal/radius"
	"github.com/ispx-limited/radblaster/internal/web"
)

func runServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	var (
		addr     = fs.String("addr", "127.0.0.1:8080", "address to serve the interface on")
		profiles = fs.String("profiles", "profiles", "directory to keep saved profiles in")
		dicts    stringList
	)
	fs.Var(&dicts, "dictionary", "dictionary file or directory to load; repeatable")
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

	dict := radius.StandardDictionary()
	if len(dicts) > 0 {
		var err error
		if dict, err = radius.LoadDictionary(dicts...); err != nil {
			return err
		}
	}

	store, err := profile.NewStore(*profiles)
	if err != nil {
		return err
	}

	srv := api.New(dict, web.FS(), store)
	defer srv.Close()

	httpSrv := &http.Server{
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		return fmt.Errorf("listening on %s: %w", *addr, err)
	}

	fmt.Fprintf(os.Stderr, "radblaster is at http://%s\n", ln.Addr())
	fmt.Fprintf(os.Stderr, "Profiles are kept in %s\n", store.Dir())
	if h, _, err := net.SplitHostPort(*addr); err == nil && (h == "" || h == "0.0.0.0" || h == "::") {
		fmt.Fprintln(os.Stderr, "Warning: this is reachable from the network and has no authentication.")
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
