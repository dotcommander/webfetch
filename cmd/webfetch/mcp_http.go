package main

import (
	"context"
	"crypto/subtle"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/dotcommander/webfetch/internal/webfetch"
	"github.com/voocel/mcp-sdk-go/transport/streamhttp"
)

const (
	mcpHTTPDefaultListen     = "127.0.0.1:8787"
	mcpBearerTokenEnv        = "WEBFETCH_MCP_BEARER_TOKEN"
	mcpHTTPReadHeaderTimeout = 5 * time.Second
	mcpHTTPIdleTimeout       = 2 * time.Minute
	mcpHTTPMaxHeaderBytes    = 64 << 10
)

type mcpHTTPConfig struct {
	Listen         string
	BearerToken    string
	AllowedOrigins []string
	Mode           mcpToolMode
}

func runMCPHTTP(ctx context.Context, args []string) error {
	cfg, err := mcpHTTPConfigFromArgs(args)
	if err != nil {
		return err
	}
	service := configuredService(webfetch.Config{})
	return runAndCloseService(service, true, func() error {
		listener, err := net.Listen("tcp", cfg.Listen)
		if err != nil {
			return fmt.Errorf("listen %s: %w", cfg.Listen, err)
		}
		server := newMCPHTTPServer(newMCPHTTPHandler(service, webfetch.ResolveVersion(version), cfg.Mode, cfg.BearerToken, cfg.AllowedOrigins))
		serveErr := make(chan error, 1)
		go func() { serveErr <- server.Serve(listener) }()

		select {
		case err := <-serveErr:
			if err == http.ErrServerClosed {
				return nil
			}
			return err
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := server.Shutdown(shutdownCtx); err != nil {
				return fmt.Errorf("shutdown MCP HTTP server: %w", err)
			}
			return nil
		}
	})
}

func newMCPHTTPServer(handler http.Handler) *http.Server {
	// Streamable HTTP responses can be long-lived, so bound request headers and
	// idle keep-alive connections without imposing a response write deadline.
	return &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: mcpHTTPReadHeaderTimeout,
		IdleTimeout:       mcpHTTPIdleTimeout,
		MaxHeaderBytes:    mcpHTTPMaxHeaderBytes,
	}
}

func mcpHTTPConfigFromArgs(args []string) (mcpHTTPConfig, error) {
	cfg := mcpHTTPConfig{
		Listen:      mcpHTTPDefaultListen,
		BearerToken: strings.TrimSpace(os.Getenv(mcpBearerTokenEnv)),
	}
	modeArgs := make([]string, 0, 1)
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--compact":
			modeArgs = append(modeArgs, args[i])
		case "--listen":
			if i+1 >= len(args) {
				return mcpHTTPConfig{}, fmt.Errorf("--listen requires an address")
			}
			i++
			cfg.Listen = args[i]
		case "--allow-origin":
			if i+1 >= len(args) {
				return mcpHTTPConfig{}, fmt.Errorf("--allow-origin requires an origin")
			}
			i++
			cfg.AllowedOrigins = append(cfg.AllowedOrigins, args[i])
		default:
			return mcpHTTPConfig{}, fmt.Errorf("--mcp-http accepts --compact, --listen, and --allow-origin")
		}
	}
	mode, err := mcpToolModeFromArgs(modeArgs)
	if err != nil {
		return mcpHTTPConfig{}, err
	}
	if err := validateMCPHTTPListen(cfg.Listen, cfg.BearerToken); err != nil {
		return mcpHTTPConfig{}, err
	}
	cfg.Mode = mode
	return cfg, nil
}

func validateMCPHTTPListen(listen, bearerToken string) error {
	host, port, err := net.SplitHostPort(strings.TrimSpace(listen))
	if err != nil {
		return fmt.Errorf("invalid MCP HTTP listen address %q: %w", listen, err)
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 0 || portNumber > 65535 {
		return fmt.Errorf("invalid MCP HTTP port %q", port)
	}
	if isLoopbackMCPHost(host) {
		return nil
	}
	if strings.TrimSpace(bearerToken) == "" {
		return fmt.Errorf("MCP HTTP listen address %q is non-loopback; set %s", listen, mcpBearerTokenEnv)
	}
	return nil
}

func isLoopbackMCPHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func newMCPHTTPHandler(service *webfetch.Service, version string, mode mcpToolMode, bearerToken string, allowedOrigins []string) http.Handler {
	srv := newMCPServerWithMode(service, version, mode)
	handler := http.Handler(streamhttp.NewHandler(srv, &streamhttp.Options{
		AllowedOrigins: allowedOrigins,
	}))
	if strings.TrimSpace(bearerToken) == "" {
		return handler
	}
	return mcpBearerAuthHandler(handler, bearerToken)
}

func mcpBearerAuthHandler(next http.Handler, expected string) http.Handler {
	expected = strings.TrimSpace(expected)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		const prefix = "Bearer "
		header := r.Header.Get("Authorization")
		provided := ""
		if strings.HasPrefix(header, prefix) {
			provided = strings.TrimSpace(strings.TrimPrefix(header, prefix))
		}
		if subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) != 1 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}
