// Command radblaster tests RADIUS authentication and accounting
// infrastructure.
package main

import (
	"context"
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/ispx-limited/radblaster/internal/radius"
	"github.com/ispx-limited/radblaster/internal/version"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "radblaster: "+err.Error())
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `radblaster tests RADIUS authentication and accounting infrastructure.

Usage:
  radblaster serve [flags]    open the web interface
  radblaster run [flags]      offer sustained load and report the result
  radblaster auth [flags]     send one Access-Request and report the reply
  radblaster version          print the build version

Run "radblaster <command> -h" for the flags of a command.
`)
}

func run(args []string) error {
	if len(args) == 0 {
		usage()
		return errors.New("no command given")
	}

	switch args[0] {
	case "serve":
		return runServe(args[1:])
	case "run":
		return runLoad(args[1:])
	case "auth":
		return runAuth(args[1:])
	case "version":
		fmt.Println(version.String())
		return nil
	case "-h", "--help", "help":
		usage()
		return nil
	default:
		usage()
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func runAuth(args []string) error {
	fs := flag.NewFlagSet("auth", flag.ContinueOnError)
	var (
		server   = fs.String("server", "", "RADIUS server as host:port (required)")
		secret   = fs.String("secret", "", "shared secret (required)")
		user     = fs.String("user", "", "User-Name (required)")
		password = fs.String("password", "", "password to authenticate with")
		chap     = fs.Bool("chap", false, "authenticate with CHAP instead of PAP")
		nasID    = fs.String("nas-id", "", "NAS-Identifier to send")
		timeout  = fs.Duration("timeout", 3*time.Second, "per-attempt timeout")
		retries  = fs.Int("retries", 2, "retransmissions after the first attempt")
	)
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Send one Access-Request and report the reply.\n\nFlags:")
		fs.PrintDefaults()
		fmt.Fprintln(os.Stderr, "\nThe shared secret is read from RADBLASTER_SECRET if -secret is not given,")
		fmt.Fprintln(os.Stderr, "which keeps it out of the process list and the shell history.")
	}
	if err := fs.Parse(args); err != nil {
		return err
	}

	if *secret == "" {
		*secret = os.Getenv("RADBLASTER_SECRET")
	}
	switch {
	case *server == "":
		return errors.New("-server is required")
	case *secret == "":
		return errors.New("a shared secret is required, via -secret or RADBLASTER_SECRET")
	case *user == "":
		return errors.New("-user is required")
	}
	if !strings.Contains(*server, ":") {
		*server += ":1812"
	}

	p, err := radius.New(radius.CodeAccessRequest, 0)
	if err != nil {
		return err
	}
	if err := setIdentifier(p); err != nil {
		return err
	}

	p.AddString(radius.AttrUserName, *user)
	if *chap {
		var ident [1]byte
		if _, err := rand.Read(ident[:]); err != nil {
			return fmt.Errorf("generating CHAP identifier: %w", err)
		}
		// With no CHAP-Challenge attribute the request authenticator is the
		// challenge, per RFC 2865 section 5.3.
		resp := radius.CHAPPassword(ident[0], []byte(*password), p.Authenticator[:])
		p.Add(radius.AttrCHAPPassword, resp[:])
	} else {
		enc, err := radius.EncryptUserPassword(nil, []byte(*password), []byte(*secret), p.Authenticator)
		if err != nil {
			return err
		}
		p.Add(radius.AttrUserPassword, enc)
	}
	if *nasID != "" {
		p.AddString(radius.AttrNASIdentifier, *nasID)
	}
	p.AddUint32(radius.AttrServiceType, 2)    // Framed-User
	p.AddUint32(radius.AttrFramedProtocol, 1) // PPP

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	c := &radius.Client{
		Addr:    *server,
		Secret:  []byte(*secret),
		Timeout: *timeout,
		Retries: *retries,
	}

	started := time.Now()
	reply, err := c.Exchange(ctx, p)
	elapsed := time.Since(started)
	if err != nil {
		return err
	}

	dict := radius.StandardDictionary()
	fmt.Printf("%s in %s\n", reply.Code, elapsed.Round(time.Microsecond))
	for _, a := range reply.Attributes {
		fmt.Printf("  %-28s %s\n",
			dict.VendorAttrName(a.Vendor, a.Type),
			dict.FormatVendorValue(a.Vendor, a.Type, a.Value))
	}

	if reply.Code != radius.CodeAccessAccept {
		return fmt.Errorf("expected Access-Accept, got %s", reply.Code)
	}
	return nil
}

// setIdentifier picks an unpredictable starting identifier so repeated runs do
// not collide with a server's duplicate-detection cache.
func setIdentifier(p *radius.Packet) error {
	var b [1]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Errorf("generating identifier: %w", err)
	}
	p.Identifier = b[0]
	return nil
}
