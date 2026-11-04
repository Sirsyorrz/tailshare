package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"maps"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"tailscale.com/client/local"
	"tailscale.com/tsnet"

	"github.com/Sirsyorrz/tailshare/server/internal/activity"
	"github.com/Sirsyorrz/tailshare/server/internal/config"
	"github.com/Sirsyorrz/tailshare/server/internal/identity"
	"github.com/Sirsyorrz/tailshare/server/internal/reach"
	"github.com/Sirsyorrz/tailshare/server/internal/thumb"
	"github.com/Sirsyorrz/tailshare/server/internal/web"
)

// version is set at release build time with -ldflags "-X main.version=...".
var version = "dev"

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
	rootsFlag := flag.String("roots", "", "comma-separated folders shares may be created in from the admin API (default: home folder, unless -roots-file is set)")
	rootsFile := flag.String("roots-file", "", "file listing more allowed folders, one per line, re-read on every use")
	bootstrapAdmin := flag.String("bootstrap-admin", "", "login or id:<userid> made admin when the config has no admins")
	flag.BoolVar(&eventsOut, "events", false, "print machine-readable TAILSHARE-EVENT lines on stdout (for the desktop app)")
	statusFile := flag.String("status-file", "", "keep this JSON file updated with the server's state (for the desktop app)")
	logFile := flag.String("log-file", "", "also write the log to this file")
	exitOnEOF := flag.Bool("exit-on-eof", false, "exit when stdin closes, so a parent app's crash can't leave the server running")
	ffmpegPath := flag.String("ffmpeg", "", "ffmpeg to use for video and HEIC/AVIF thumbnails, before the one on PATH; may not exist yet")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println(version)
		return
	}

	thumb.SetFFmpeg(*ffmpegPath)
	if *cfgPath == "" {
		*cfgPath = filepath.Join(*stateDir, "config.json")
	}
	if *logFile != "" {
		if err := openLogFile(*logFile); err != nil {
			log.Fatal(err)
		}
	}
	if *statusFile != "" {
		status = newStatusWriter(*statusFile)
		status.set(func(st *statusDoc) { st.Phase = "starting"; st.Hostname = *hostname })
		defer status.set(func(st *statusDoc) { st.Phase = "stopped"; st.URL, st.LoginURL = "", "" })
	}
	if *rootsFlag == "" && *rootsFile == "" {
		*rootsFlag, _ = os.UserHomeDir()
	}
	// The state dir holds the node key; a copy lets someone impersonate the node.
	if err := os.MkdirAll(*stateDir, 0o700); err != nil {
		fatal(err)
	}
	if err := os.Chmod(*stateDir, 0o700); err != nil {
		fatal(err)
	}
	// Two servers on one state dir would fight over the same Tailscale node.
	unlock, err := lockDir(*stateDir)
	if err != nil {
		fatal(fmt.Errorf("another tailshare server is already using %s", *stateDir))
	}
	defer unlock()
	if _, err := os.Stat(*cfgPath); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(*cfgPath, []byte(emptyConfig), 0o600); err != nil {
			fatal(err)
		}
		log.Printf("created empty config at %s", *cfgPath)
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fatal(err)
	}
	if *bootstrapAdmin != "" && len(cfg.Admins) == 0 {
		cfg.Admins = []string{*bootstrapAdmin}
		if err := cfg.Save(*cfgPath); err != nil {
			fatal(err)
		}
		log.Printf("made %s admin", *bootstrapAdmin)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *exitOnEOF {
		go func() {
			io.Copy(io.Discard, os.Stdin)
			log.Print("stdin closed, shutting down")
			stop()
		}()
	}

	act, err := activity.Open(filepath.Join(*stateDir, "activity.log"))
	if err != nil {
		fatal(err)
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
		fatal(err)
	}
	srv.Configure(*cfgPath, splitRoots(*rootsFlag), *rootsFile)
	srv.Load(cfg)
	go srv.Housekeep(ctx)
	go srv.WatchReach(ctx)
	go reloadOnSignal(ctx, srv)

	errc := make(chan error, 1)
	go func() { errc <- serve() }()
	select {
	case err := <-errc:
		fatal(err)
	case <-ctx.Done():
		log.Print("shutting down")
		event("stopped", nil)
	}
}

// eventsOut is set by -events. The desktop app reads these lines instead of
// scraping human-readable logs.
var eventsOut bool

func event(kind string, fields map[string]string) {
	status.set(func(st *statusDoc) {
		switch kind {
		case "login":
			st.Phase, st.LoginURL, st.Notice = "login", fields["url"], fields["reason"]
		case "approval":
			st.Phase, st.LoginURL, st.Notice = "approval", "", fields["reason"]
		case "serving":
			st.Phase, st.URL, st.LoginURL, st.Notice = "running", fields["url"], "", ""
		case "warning":
			st.Warning = fields["message"]
		case "error":
			st.Phase, st.Error = "failed", fields["message"]
		case "stopped":
			st.Phase, st.URL, st.LoginURL = "stopped", "", ""
		}
	})
	if !eventsOut {
		return
	}
	m := map[string]string{"event": kind}
	maps.Copy(m, fields)
	b, _ := json.Marshal(m)
	fmt.Fprintf(os.Stdout, "TAILSHARE-EVENT %s\n", b)
}

func fatal(err error) {
	event("error", map[string]string{"message": err.Error()})
	log.Fatal(err)
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

func reloadOnSignal(ctx context.Context, srv *web.Server) {
	c := make(chan os.Signal, 1)
	notifyReload(c)
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
	event("serving", map[string]string{"url": "http://" + addr})
	srv := web.New(web.Options{
		Resolver: identity.Fixed{User: identity.User{ID: 1, Login: user, Display: user, Node: "dev"}},
		Hosts:    []string{addr},
		Activity: act,
		Version:  version,
	})
	return srv, func() error { return httpServer(srv.Handler()).Serve(ln) }, nil
}

// dumpOnSignal logs the peers this node can see and the access rules it was
// given, for working out why someone can't connect.
func dumpOnSignal(ctx context.Context, lc *local.Client) {
	c := make(chan os.Signal, 1)
	notifyDump(c)
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
		if _, url, ok := strings.Cut(msg, "go to: "); ok && strings.HasPrefix(url, "https://") {
			event("login", map[string]string{"url": strings.TrimSpace(url)})
		}
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
	var current atomic.Pointer[string]
	current.Store(&fqdn)
	lc, err := ts.LocalClient()
	if err != nil {
		return nil, nil, err
	}
	// Issuing the cert can take over a minute; do it now rather than inside
	// the first visitor's TLS handshake, where browsers give up.
	prefetchCert := func(name string) {
		if _, _, err := lc.CertPair(ctx, name); err != nil {
			log.Printf("fetching HTTPS cert: %v", err)
			return
		}
		log.Printf("HTTPS cert ready for %s", name)
	}
	go prefetchCert(fqdn)

	plain, err := ts.Listen("tcp", ":80")
	if err != nil {
		return nil, nil, err
	}
	redirect := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://"+*current.Load()+r.URL.RequestURI(), http.StatusMovedPermanently)
	})
	go httpServer(redirect).Serve(plain)

	go dumpOnSignal(ctx, lc)

	log.Printf("serving https://%s", fqdn)
	event("serving", map[string]string{"url": "https://" + fqdn})
	srv := web.New(web.Options{
		Resolver: identity.Tailscale{LC: lc},
		Hosts:    []string{fqdn},
		HTTPS:    true,
		Activity: act,
		Reach:    func(ctx context.Context) (reach.Report, error) { return reach.Check(ctx, lc, 443) },
		Version:  version,
	})
	watch := newNodeWatch(lc, fqdn, func(name string) {
		srv.SetHosts(name)
		current.Store(&name)
		go prefetchCert(name)
	})
	go watch.run(ctx, lc)
	return srv, func() error { return httpServer(srv.Handler()).Serve(ln) }, nil
}
