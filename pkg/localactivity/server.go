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
	Address           = "127.0.0.1:3333"
	BaseURL           = "http://" + Address
	healthHeaderName  = "X-Gesta-Agent"
	healthHeaderValue = "activity-ui-v1"
)

type Server struct {
	httpServer *http.Server
}

func Start(dataDir string, logger *slog.Logger) (*Server, error) {
	if logger == nil {
		logger = slog.Default()
	}
	listener, err := net.Listen("tcp4", Address)
	if err != nil {
		return nil, fmt.Errorf("listen on %s: %w", Address, err)
	}
	handler := newHandler(activitydetail.NewStore(dataDir))
	server := &Server{
		httpServer: &http.Server{
			Addr:              Address,
			Handler:           handler,
			ReadHeaderTimeout: 2 * time.Second,
			ReadTimeout:       3 * time.Second,
			WriteTimeout:      3 * time.Second,
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

func Healthy(parent context.Context) bool {
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
	return response.StatusCode == http.StatusNoContent &&
		response.Header.Get(healthHeaderName) == healthHeaderValue
}

type handler struct {
	store    activitydetail.Store
	template *template.Template
}

func newHandler(store activitydetail.Store) http.Handler {
	return handler{
		store:    store,
		template: pageTemplates,
	}
}

func (h handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	setSecurityHeaders(writer.Header())
	if !allowedHost(request.Host) {
		http.Error(writer, "Misdirected Request", http.StatusMisdirectedRequest)
		return
	}
	if request.Method != http.MethodGet && request.Method != http.MethodHead {
		writer.Header().Set("Allow", "GET, HEAD")
		http.Error(writer, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}
	if request.URL.Path == "/healthz" {
		writer.Header().Set(healthHeaderName, healthHeaderValue)
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
	if err := h.template.ExecuteTemplate(writer, "activity.html", view); err != nil {
		return
	}
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
