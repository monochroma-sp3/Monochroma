package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/httprate"
	"github.com/navidrome/navidrome/conf"
	"github.com/navidrome/navidrome/consts"
	"github.com/navidrome/navidrome/core/auth"
	"github.com/navidrome/navidrome/core/metrics"
	"github.com/navidrome/navidrome/log"
	"github.com/navidrome/navidrome/model"
	"github.com/navidrome/navidrome/server/events"
	"github.com/navidrome/navidrome/ui"
)

type Server struct {
	router     chi.Router
	ds         model.DataStore
	appRoot    string
	feishinDir string // Feishin web build dir served at root, empty when disabled
	broker     events.Broker
	insights   metrics.Insights
}

func New(ds model.DataStore, broker events.Broker, insights metrics.Insights) *Server {
	s := &Server{ds: ds, broker: broker, insights: insights}
	initialSetup(ds)
	auth.Init(s.ds)
	s.initRoutes()
	s.mountAuthenticationRoutes()
	s.mountRootRedirector()
	checkFFmpegInstallation()
	checkExternalCredentials()
	return s
}

func (s *Server) MountRouter(description, urlPath string, subRouter http.Handler) {
	urlPath = path.Join(conf.Server.BasePath, urlPath)
	log.Info(fmt.Sprintf("Mounting %s routes", description), "path", urlPath)
	s.router.Group(func(r chi.Router) {
		r.Mount(urlPath, subRouter)
	})
}

// Run starts the server with the given address, and if specified, with TLS enabled.
func (s *Server) Run(ctx context.Context, addr string, port int, tlsCert string, tlsKey string) error {
	// Mount the router for the frontend assets
	s.MountRouter("WebUI", consts.URLPathUI, s.frontendAssetsHandler())

	// Create a new http.Server with the specified read header timeout and handler
	server := &http.Server{
		ReadHeaderTimeout: consts.ServerReadHeaderTimeout,
		Handler:           s.router,
	}

	// Determine if TLS is enabled
	tlsEnabled := tlsCert != "" && tlsKey != ""

	// Validate TLS certificates before starting the server
	if tlsEnabled {
		if err := validateTLSCertificates(tlsCert, tlsKey); err != nil {
			return err
		}
	}

	// Create a listener based on the address type (either Unix socket or TCP)
	var listener net.Listener
	var err error
	if after, ok := strings.CutPrefix(addr, "unix:"); ok {
		socketPath := after
		listener, err = createUnixSocketFile(socketPath, conf.Server.UnixSocketPerm)
		if err != nil {
			return err
		}
	} else {
		addr = fmt.Sprintf("%s:%d", addr, port)
		listener, err = net.Listen("tcp", addr)
		if err != nil {
			return fmt.Errorf("creating tcp listener: %w", err)
		}
	}

	// Start the server in a new goroutine and send an error signal to errC if there's an error
	errC := make(chan error)
	go func() {
		var err error
		if tlsEnabled {
			// Start the HTTPS server
			log.Info("Starting server with TLS (HTTPS) enabled", "tlsCert", tlsCert, "tlsKey", tlsKey)
			err = server.ServeTLS(listener, tlsCert, tlsKey)
		} else {
			// Start the HTTP server
			err = server.Serve(listener)
		}
		if !errors.Is(err, http.ErrServerClosed) {
			errC <- err
		}
	}()

	// Measure server startup time
	startupTime := time.Since(consts.ServerStart)

	// Wait a short time to make sure the server has started successfully
	select {
	case err := <-errC:
		log.Error(ctx, "Could not start server. Aborting", err)
		return fmt.Errorf("starting server: %w", err)
	case <-time.After(50 * time.Millisecond):
		log.Info(ctx, "----> Navidrome server is ready!", "address", addr, "startupTime", startupTime, "tlsEnabled", tlsEnabled)
	}

	// Wait for a signal to terminate
	select {
	case err := <-errC:
		return fmt.Errorf("running server: %w", err)
	case <-ctx.Done():
		// If the context is done (i.e. the server should stop), proceed to shutting down the server
	}

	// Try to stop the HTTP server gracefully
	log.Info(ctx, "Stopping HTTP server")
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	server.SetKeepAlivesEnabled(false)
	if err := server.Shutdown(ctx); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		log.Error(ctx, "Unexpected error in http.Shutdown()", err)
	}
	return nil
}

func createUnixSocketFile(socketPath string, socketPerm string) (net.Listener, error) {
	// Remove the socket file if it already exists
	if err := os.Remove(socketPath); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("removing previous unix socket file: %w", err)
	}
	// Create listener
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return nil, fmt.Errorf("creating unix socket listener: %w", err)
	}
	// Converts the socketPerm to uint and updates the permission of the unix socket file
	perm, err := strconv.ParseUint(socketPerm, 8, 32)
	if err != nil {
		return nil, fmt.Errorf("parsing unix socket file permissions: %w", err)
	}
	err = os.Chmod(socketPath, os.FileMode(perm))
	if err != nil {
		return nil, fmt.Errorf("updating permission of unix socket file: %w", err)
	}
	return listener, nil
}

func (s *Server) initRoutes() {
	s.appRoot = path.Join(conf.Server.BasePath, consts.URLPathUI)
	s.feishinDir = resolveFeishinDir()

	r := chi.NewRouter()

	defaultMiddlewares := chi.Middlewares{
		secureMiddleware(),
		corsHandler(),
		middleware.RequestID,
		realIPMiddleware,
		middleware.Recoverer,
		middleware.Heartbeat("/ping"),
		robotsTXT(robotsFS()),
		serverAddressMiddleware,
		clientUniqueIDMiddleware,
		compressMiddleware(),
		loggerInjector,
		JWTVerifier,
	}

	// Mount the Native API /events endpoint with all default middlewares, adding the authentication middlewares
	if conf.Server.DevActivityPanel {
		r.Group(func(r chi.Router) {
			r.Use(defaultMiddlewares...)
			r.Use(Authenticator(s.ds))
			r.Use(JWTRefresher)
			r.Handle(path.Join(conf.Server.BasePath, consts.URLPathNativeAPI, "events"), s.broker)
		})
	}

	// Configure the router with the default middlewares and requestLogger
	r.Group(func(r chi.Router) {
		r.Use(defaultMiddlewares...)
		r.Use(requestLogger)
		s.router = r
	})
}

func (s *Server) mountAuthenticationRoutes() chi.Router {
	r := s.router
	return r.Route(path.Join(conf.Server.BasePath, "/auth"), func(r chi.Router) {
		if conf.Server.AuthRequestLimit > 0 {
			log.Info("Login rate limit set", "requestLimit", conf.Server.AuthRequestLimit,
				"windowLength", conf.Server.AuthWindowLength)

			rateLimiter := httprate.LimitByIP(conf.Server.AuthRequestLimit, conf.Server.AuthWindowLength)
			r.With(rateLimiter).Post("/login", login(s.ds))
			if conf.Server.EnablePublicRegistration {
				// Registration is abusable (mass account creation), so it gets
				// the same per-IP limiter as login.
				r.With(rateLimiter).Post("/register", register(s.ds))
			}
		} else {
			log.Warn("Login rate limit is disabled! Consider enabling it to be protected against brute-force attacks")

			r.Post("/login", login(s.ds))
			if conf.Server.EnablePublicRegistration {
				r.Post("/register", register(s.ds))
			}
		}
		r.Post("/createAdmin", createAdmin(s.ds))
	})
}

// Serve UI app assets
func (s *Server) mountRootRedirector() {
	r := s.router

	// When Feishin is enabled it IS the site root: serve its SPA directly (no
	// redirect) plus a generated settings.js that pins it to this server.
	// Navidrome's own UI stays reachable at /app for administration.
	if s.feishinDir != "" {
		r.Get(path.Join(conf.Server.BasePath, "/settings.js"), s.feishinSettings)
		r.Handle("/*", s.feishinRootHandler())
		r.Get(s.appRoot, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, s.appRoot+"/", http.StatusFound)
		})
		return
	}

	// Default: redirect root (and any unmatched path) to Navidrome's UI.
	r.Get("/*", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, s.appRoot+"/", http.StatusFound)
	})
	r.Get(s.appRoot, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, s.appRoot+"/", http.StatusFound)
	})
}

func (s *Server) frontendAssetsHandler() http.Handler {
	r := chi.NewRouter()

	r.Handle("/", Index(s.ds, ui.BuildAssets()))
	r.Handle("/*", http.StripPrefix(s.appRoot, http.FileServer(http.FS(ui.BuildAssets()))))
	return r
}

// resolveFeishinDir returns Feishin's web build directory, or "" when Feishin is
// disabled or its build (index.html) cannot be found.
// robotsFS picks where /robots.txt is served from. When Feishin is the public
// UI, its build dir owns the site's crawling policy (SEO-friendly robots.txt
// shipped with the web build); otherwise fall back to the embedded admin UI
// assets, whose robots.txt disallows everything.
func robotsFS() fs.FS {
	if dir := resolveFeishinDir(); dir != "" {
		if _, err := os.Stat(filepath.Join(dir, "robots.txt")); err == nil {
			return os.DirFS(dir)
		}
	}
	return ui.BuildAssets()
}

func resolveFeishinDir() string {
	if !conf.Server.Feishin.Enabled {
		return ""
	}
	dir := conf.Server.Feishin.Path
	if dir == "" {
		log.Warn("Feishin UI enabled but Feishin.Path is empty; serving Navidrome UI")
		return ""
	}
	if _, err := os.Stat(filepath.Join(dir, "index.html")); err != nil {
		log.Warn("Feishin UI enabled but web build not found; serving Navidrome UI", "path", dir, err)
		return ""
	}
	log.Info("Serving Feishin as the default UI at site root", "path", dir)
	return dir
}

// feishinRootHandler serves Feishin's static web build from disk at the site
// root as a SPA: existing files are served directly, anything else falls back to
// index.html for Feishin's client-side router. Navidrome's own routes (/app,
// /rest, /api, ...) are more specific and still take precedence.
func (s *Server) feishinRootHandler() http.Handler {
	dir := s.feishinDir
	indexFile := filepath.Join(dir, "index.html")
	fileServer := http.FileServer(http.Dir(dir))
	spa := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rel := path.Clean("/" + r.URL.Path)
		if rel != "/" {
			if info, err := os.Stat(filepath.Join(dir, filepath.FromSlash(rel))); err == nil && !info.IsDir() {
				fileServer.ServeHTTP(w, r)
				return
			}
		}
		http.ServeFile(w, r, indexFile) // SPA fallback (and root)
	})
	if conf.Server.BasePath != "" {
		return http.StripPrefix(conf.Server.BasePath, spa)
	}
	return spa
}

// feishinSettings serves a generated settings.js that pins Feishin's web build to
// this Navidrome server (SERVER_LOCK), so it skips the server-picker and only
// asks the user to log in once. SERVER_URL is derived from the request so it
// works whether reached via localhost, a LAN IP, or a reverse proxy.
func (s *Server) feishinSettings(w http.ResponseWriter, r *http.Request) {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if xfp := r.Header.Get("X-Forwarded-Proto"); xfp != "" {
		scheme = xfp
	}
	origin := scheme + "://" + r.Host

	registration := "false"
	if conf.Server.EnablePublicRegistration {
		registration = "true"
	}

	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = fmt.Fprintf(w, `"use strict";
window.SERVER_URL = %q;
window.SERVER_NAME = "Monochroma";
window.SERVER_TYPE = "navidrome";
window.SERVER_LOCK = "true";
window.LEGACY_AUTHENTICATION = "false";
window.ANALYTICS_DISABLED = "true";
window.ENABLE_REGISTRATION = %q;
window.TRANSFER_URL = %q;
`, origin, registration, conf.Server.Feishin.TransferURL)
}

// validateTLSCertificates validates the TLS certificate and key files before starting the server.
// It provides detailed error messages for common issues like encrypted private keys.
func validateTLSCertificates(certFile, keyFile string) error {
	// Read the key file to check for encryption
	keyData, err := os.ReadFile(keyFile) //nolint:gosec
	if err != nil {
		return fmt.Errorf("reading TLS key file: %w", err)
	}

	// Parse PEM blocks and check for encryption
	block, _ := pem.Decode(keyData)
	if block == nil {
		return errors.New("TLS key file does not contain a valid PEM block")
	}

	// Check for encrypted private key indicators
	if isEncryptedPEM(block, keyData) {
		return errors.New("TLS private key is encrypted (password-protected). " +
			"Navidrome does not support encrypted private keys. " +
			"Please decrypt your key using: openssl pkey -in <encrypted-key> -out <decrypted-key>")
	}

	// Try to load the certificate pair to validate it
	_, err = tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return fmt.Errorf("loading TLS certificate/key pair: %w", err)
	}

	return nil
}

// isEncryptedPEM checks if a PEM block represents an encrypted private key.
func isEncryptedPEM(block *pem.Block, rawData []byte) bool {
	// Check for PKCS#8 encrypted format (BEGIN ENCRYPTED PRIVATE KEY)
	if block.Type == "ENCRYPTED PRIVATE KEY" {
		return true
	}

	// Check for legacy encrypted format with Proc-Type header
	if block.Headers != nil {
		if procType, ok := block.Headers["Proc-Type"]; ok && strings.Contains(procType, "ENCRYPTED") {
			return true
		}
	}

	// Also check raw data for DEK-Info header (in case pem.Decode doesn't parse headers correctly)
	if bytes.Contains(rawData, []byte("DEK-Info:")) || bytes.Contains(rawData, []byte("Proc-Type: 4,ENCRYPTED")) {
		return true
	}

	return false
}
