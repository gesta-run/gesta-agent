package localactivity

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/activitydetail"
)

const (
	Address            = "127.0.0.1:3333"
	BaseURL            = "http://" + Address
	healthHeaderName   = "X-Gesta-Agent"
	healthHeaderValue  = "activity-ui-v2"
	daemonHeaderName   = "X-Gesta-Daemon-ID"
	memoryHeaderName   = "X-Gesta-Memory"
	memoryHeaderValue  = "proxy-v1"
	ActivityHeaderName = "X-Gesta-Activity-ID"
)

type Server struct {
	httpServer *http.Server
}

func Start(dataDir, daemonID string, logger *slog.Logger) (*Server, error) {
	return StartWithMemory(dataDir, daemonID, logger, nil)
}

func StartWithMemory(dataDir, daemonID string, logger *slog.Logger, memory MemoryService) (*Server, error) {
	if logger == nil {
		logger = slog.Default()
	}
	listener, err := net.Listen("tcp4", Address)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", Address, err)
	}
	handler := newHandlerWithMemory(activitydetail.NewStore(dataDir), daemonID, memory)
	server := &Server{
		httpServer: &http.Server{
			Addr:              Address,
			Handler:           handler,
			ReadHeaderTimeout: 2 * time.Second,
			ReadTimeout:       3 * time.Second,
			WriteTimeout:      195 * time.Second,
			IdleTimeout:       15 * time.Second,
			MaxHeaderBytes:    8 * 1024,
		},
	}
	go func() {
		if serveErr := server.httpServer.Serve(listener); serveErr != nil &&
			!errors.Is(serveErr, http.ErrServerClosed) {
			logger.Warn("local activity server stopped", "error", serveErr)
		}
	}()
	logger.Info("local activity server started", "url", BaseURL)
	return server, nil
}

func (s *Server) Close(ctx context.Context) error {
	if s == nil || s.httpServer == nil {
		return nil
	}
	return s.httpServer.Shutdown(ctx)
}

func ActivityURL(activityID string) string {
	return BaseURL + "/activity/" + url.PathEscape(strings.TrimSpace(activityID))
}

func NoticeURL() string {
	return BaseURL + "/api/v1/activity/notice"
}

func Healthy(parent context.Context) bool {
	return HealthyFor(parent, "")
}

func HealthyFor(parent context.Context, daemonID string) bool {
	return healthyFor(parent, daemonID, false)
}

func MemoryHealthyFor(parent context.Context, daemonID string) bool {
	return healthyFor(parent, daemonID, true)
}

func healthyFor(parent context.Context, daemonID string, requireMemory bool) bool {
	ctx, cancel := context.WithTimeout(parent, 75*time.Millisecond)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, BaseURL+"/healthz", nil)
	if err != nil {
		return false
	}
	client := &http.Client{
		Transport: &http.Transport{
			Proxy:             nil,
			DisableKeepAlives: true,
		},
	}
	response, err := client.Do(request)
	if err != nil {
		return false
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNoContent ||
		response.Header.Get(healthHeaderName) != healthHeaderValue {
		return false
	}
	if requireMemory && response.Header.Get(memoryHeaderName) != memoryHeaderValue {
		return false
	}
	daemonID = strings.TrimSpace(daemonID)
	return daemonID == "" || response.Header.Get(daemonHeaderName) == daemonID
}

type handler struct {
	store    activitydetail.Store
	template *template.Template
	daemonID string
	memory   MemoryService
}

func newHandlerWithDaemonID(store activitydetail.Store, daemonID string) http.Handler {
	return newHandlerWithMemory(store, daemonID, nil)
}

func newHandlerWithMemory(store activitydetail.Store, daemonID string, memory MemoryService) http.Handler {
	return handler{
		store:    store,
		template: pageTemplates,
		daemonID: strings.TrimSpace(daemonID),
		memory:   memory,
	}
}

func (h handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	setSecurityHeaders(writer.Header())
	if !allowedHost(request.Host) {
		http.Error(writer, "Misdirected Request", http.StatusMisdirectedRequest)
		return
	}
	if request.URL.Path == "/api/v1/activity/notice" {
		h.serveActivityNotice(writer, request)
		return
	}
	if strings.HasPrefix(request.URL.Path, "/api/v1/memory/") {
		h.serveMemory(writer, request)
		return
	}
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		writer.Header().Set("Allow", "GET, HEAD")
		http.Error(writer, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if request.URL.Path == "/healthz" {
		writer.Header().Set(healthHeaderName, healthHeaderValue)
		if h.daemonID != "" {
			writer.Header().Set(daemonHeaderName, h.daemonID)
		}
		if h.memory != nil {
			writer.Header().Set(memoryHeaderName, memoryHeaderValue)
		}
		writer.WriteHeader(http.StatusNoContent)
		return
	}
	const activityPrefix = "/activity/"
	if !strings.HasPrefix(request.URL.Path, activityPrefix) {
		h.renderUnavailable(writer, request.Method, http.StatusNotFound)
		return
	}
	activityID := strings.TrimPrefix(request.URL.Path, activityPrefix)
	if activityID == "" || strings.Contains(activityID, "/") {
		h.renderUnavailable(writer, request.Method, http.StatusNotFound)
		return
	}
	detail, err := h.store.Get(activityID)
	if err != nil {
		h.renderUnavailable(writer, request.Method, http.StatusNotFound)
		return
	}
	view := newActivityView(detail)
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.WriteHeader(http.StatusOK)
	if request.Method == http.MethodHead {
		return
	}
	_ = h.template.ExecuteTemplate(writer, "activity.html", view)
}

func (h handler) renderUnavailable(writer http.ResponseWriter, method string, status int) {
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	writer.WriteHeader(status)
	if method == http.MethodHead {
		return
	}
	_ = h.template.ExecuteTemplate(writer, "unavailable.html", nil)
}

func setSecurityHeaders(header http.Header) {
	header.Set(
		"Content-Security-Policy",
		"default-src 'none'; style-src 'unsafe-inline'; base-uri 'none'; "+
			"form-action 'none'; frame-ancestors 'none'",
	)
	header.Set("Cache-Control", "no-store")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Referrer-Policy", "no-referrer")
	header.Set("Cross-Origin-Resource-Policy", "same-origin")
}

func allowedHost(rawHost string) bool {
	host := strings.TrimSpace(strings.ToLower(rawHost))
	if parsedHost, port, err := net.SplitHostPort(host); err == nil {
		return port == "3333" && (parsedHost == "127.0.0.1" || parsedHost == "localhost")
	}
	return host == "127.0.0.1" || host == "localhost"
}
