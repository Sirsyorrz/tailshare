package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"tailscale.com/client/local"
	"tailscale.com/tsnet"

	"github.com/Sirsyorrz/tailshare/server/internal/activity"
	"github.com/Sirsyorrz/tailshare/server/internal/config"
	"github.com/Sirsyorrz/tailshare/server/internal/identity"
	"github.com/Sirsyorrz/tailshare/server/internal/reach"
	"github.com/Sirsyorrz/tailshare/server/internal/web"
)

const emptyConfig = `{
  "admins": [],
  "shares": []
}
`

func main() {
	defState := "tailshare"
	if d, err := os.UserConfigDir(); err == nil {
		defState = filepath.Join(d, "tailshare")
	}
	stateDir := flag.String("state", defState, "state directory (Tailscale node identity, config)")
	cfgPath := flag.String("config", "", "config file (default <state>/config.json)")
	hostname := flag.String("hostname", "tailshare", "Tailscale node name")
	verbose := flag.Bool("v", false, "verbose Tailscale logs")
	devListen := flag.String("dev-listen", "", "dev only: serve plain HTTP on this loopback address without Tailscale")
	devUser := flag.String("dev-user", "dev@local", "dev only: identity every request gets")
	defRoots, _ := os.UserHomeDir()
	rootsFlag := flag.String("roots", defRoots, "comma-separated folders shares may be created in from the admin API")
	flag.Parse()

	if *cfgPath == "" {
		*cfgPath = filepath.Join(*stateDir, "config.json")
	}
	// The state dir holds the node key; a copy lets someone impersonate the node.
	if err := os.MkdirAll(*stateDir, 0o700); err != nil {
		log.Fatal(err)
	}
	if err := os.Chmod(*stateDir, 0o700); err != nil {
		log.Fatal(err)
	}
	if _, err := os.Stat(*cfgPath); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(*cfgPath, []byte(emptyConfig), 0o600); err != nil {
			log.Fatal(err)
		}
		log.Printf("created empty config at %s", *cfgPath)
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatal(err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	act, err := activity.Open(filepath.Join(*stateDir, "activity.log"))
	if err != nil {
		log.Fatal(err)
	}
	defer act.Close()

	var srv *web.Server
	var serve func() error
	if *devListen != "" {
		srv, serve, err = devMode(*devListen, *devUser, act)
	} else {
		srv, serve, err = tailscaleMode(ctx, *stateDir, *hostname, *verbose, act)
	}
	if err != nil {
		log.Fatal(err)
	}
	srv.Configure(*cfgPath, splitRoots(*rootsFlag))
	srv.Load(cfg)
	go srv.Housekeep(ctx)
	go srv.WatchReach(ctx)
	go reloadOnHUP(ctx, srv)

	errc := make(chan error, 1)
	go func() { errc <- serve() }()
	select {
	case err := <-errc:
		log.Fatal(err)
	case <-ctx.Done():
		log.Print("shutting down")
	}
}

func splitRoots(s string) []string {
	var out []string
	for _, r := range strings.Split(s, ",") {
		if r = strings.TrimSpace(r); r != "" {
			out = append(out, r)
		}
	}
	return out
}

func reloadOnHUP(ctx context.Context, srv *web.Server) {
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGHUP)
	for {
		select {
		case <-ctx.Done():
			return
		case <-c:
			if err := srv.Reload(); err != nil {
				log.Printf("reload failed, keeping old config: %v", err)
			}
		}
	}
}

func httpServer(h http.Handler) *http.Server {
	// No read/write timeouts: chunks and downloads can be large and slow.
	return &http.Server{Handler: h, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 2 * time.Minute}
}

func devMode(addr, user string, act *activity.Log) (*web.Server, func() error, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, nil, err
	}
	if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
		return nil, nil, fmt.Errorf("-dev-listen must be a loopback address, got %q", addr)
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, nil, err
	}
	log.Printf("DEV MODE: no Tailscale, every request is %s. http://%s", user, addr)
	srv := web.New(web.Options{
		Resolver: identity.Fixed{User: identity.User{ID: 1, Login: user, Display: user, Node: "dev"}},
		Hosts:    []string{addr},
		Activity: act,
	})
	return srv, func() error { return httpServer(srv.Handler()).Serve(ln) }, nil
}

// dumpOnUSR1 logs the peers this node can see and the access rules it was
// given, for working out why someone can't connect.
func dumpOnUSR1(ctx context.Context, lc *local.Client) {
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGUSR1)
	for {
		select {
		case <-ctx.Done():
			return
		case <-c:
		}
		st, err := lc.Status(ctx)
		if err != nil {
			log.Printf("dump: %v", err)
			continue
		}
		for _, p := range st.Peer {
			u := st.User[p.UserID]
			log.Printf("dump peer %s user=%s ips=%v online=%v shareeNode=%v relay=%q addr=%q",
				p.DNSName, u.LoginName, p.TailscaleIPs, p.Online, p.ShareeNode, p.Relay, p.CurAddr)
		}
		rules, err := lc.DebugPacketFilterRules(ctx)
		if err != nil {
			log.Printf("dump: %v", err)
			continue
		}
		for _, r := range rules {
			log.Printf("dump rule src=%v dst=%v", r.SrcIPs, r.DstPorts)
		}
	}
}

// dedupLogf drops consecutive repeats; tsnet re-prints the login URL every
// few seconds until it is approved.
func dedupLogf() func(string, ...any) {
	var mu sync.Mutex
	var last string
	return func(format string, args ...any) {
		msg := fmt.Sprintf(format, args...)
		mu.Lock()
		defer mu.Unlock()
		if msg == last {
			return
		}
		last = msg
		log.Print(msg)
	}
}

func tailscaleMode(ctx context.Context, stateDir, hostname string, verbose bool, act *activity.Log) (*web.Server, func() error, error) {
	ts := &tsnet.Server{Hostname: hostname, Dir: filepath.Join(stateDir, "tsnet"), UserLogf: dedupLogf()}
	if verbose {
		ts.Logf = log.Printf
	}
	go func() {
		<-ctx.Done()
		ts.Close()
	}()
	log.Print("starting Tailscale (first run prints a login URL)")
	ln, err := ts.ListenTLS("tcp", ":443")
	if err != nil {
		return nil, nil, err
	}
	domains := ts.CertDomains()
	if len(domains) == 0 {
		return nil, nil, errors.New("no HTTPS cert domain for this node")
	}
	fqdn := domains[0]
	lc, err := ts.LocalClient()
	if err != nil {
		return nil, nil, err
	}
	// Issuing the cert can take over a minute; do it now rather than inside
	// the first visitor's TLS handshake, where browsers give up.
	go func() {
		if _, _, err := lc.CertPair(ctx, fqdn); err != nil {
			log.Printf("fetching HTTPS cert: %v", err)
			return
		}
		log.Printf("HTTPS cert ready")
	}()

	plain, err := ts.Listen("tcp", ":80")
	if err != nil {
		return nil, nil, err
	}
	redirect := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://"+fqdn+r.URL.RequestURI(), http.StatusMovedPermanently)
	})
	go httpServer(redirect).Serve(plain)

	go dumpOnUSR1(ctx, lc)

	log.Printf("serving https://%s", fqdn)
	srv := web.New(web.Options{
		Resolver: identity.Tailscale{LC: lc},
		Hosts:    []string{fqdn},
		HTTPS:    true,
		Activity: act,
		Reach:    func(ctx context.Context) ([]reach.Peer, error) { return reach.Check(ctx, lc, 443) },
	})
	return srv, func() error { return httpServer(srv.Handler()).Serve(ln) }, nil
}
