package localactivity

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/activitydetail"
	"github.com/gesta-run/gesta-agent/pkg/memoryproxy"
	"github.com/gesta-run/gesta-agent/pkg/model"
	"github.com/gesta-run/gesta-agent/pkg/workspace"
)

type MemoryService interface {
	Search(context.Context, string, int, model.MemoryWorkspace) (model.MemorySearchResponse, error)
	Remember(context.Context, string, model.MemoryWorkspace) (model.MemoryRememberResponse, error)
}

type searchRequest struct {
	Query string `json:"query"`
	Limit int    `json:"limit,omitempty"`
}

type rememberRequest struct {
	Content string `json:"content"`
}

const (
	localMemorySearchTimeout   = 6 * time.Second
	localMemoryRememberTimeout = 190 * time.Second
)

func (h handler) serveMemory(writer http.ResponseWriter, request *http.Request) {
	if h.memory == nil {
		writeMemoryError(writer, http.StatusServiceUnavailable, "memory_disabled")
		return
	}
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		writeMemoryError(writer, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if !allowedBrowserSource(request) {
		writeMemoryError(writer, http.StatusForbidden, "forbidden_origin")
		return
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeMemoryError(writer, http.StatusUnsupportedMediaType, "content_type_must_be_application_json")
		return
	}
	workspaceContext := workspace.Resolve(request.Header.Get("X-Gesta-Cwd"))
	switch request.URL.Path {
	case "/api/v1/memory/search":
		h.serveMemorySearch(writer, request, workspaceContext)
	case "/api/v1/memory/remember":
		serveMemoryRemember(writer, request, h.memory, workspaceContext)
	default:
		writeMemoryError(writer, http.StatusNotFound, "not_found")
	}
}

func (h handler) serveMemorySearch(writer http.ResponseWriter, request *http.Request, workspaceContext model.MemoryWorkspace) {
	var body searchRequest
	if !decodeLocalMemoryRequest(writer, request, &body) {
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), localMemorySearchTimeout)
	defer cancel()
	response, err := h.memory.Search(ctx, body.Query, body.Limit, workspaceContext)
	if err != nil {
		writeProxyError(writer, err)
		return
	}
	if activityID := request.Header.Get(ActivityHeaderName); activityID != "" {
		_ = h.store.RecordMemoryRecall(activityID, activitydetail.MemoryRecallSuccess, response.Memories)
	}
	writeMemoryJSON(writer, http.StatusOK, response)
}

func serveMemoryRemember(writer http.ResponseWriter, request *http.Request, service MemoryService, workspaceContext model.MemoryWorkspace) {
	var body rememberRequest
	if !decodeLocalMemoryRequest(writer, request, &body) {
		return
	}
	ctx, cancel := context.WithTimeout(request.Context(), localMemoryRememberTimeout)
	defer cancel()
	response, err := service.Remember(ctx, body.Content, workspaceContext)
	if err != nil {
		writeProxyError(writer, err)
		return
	}
	writeMemoryJSON(writer, http.StatusOK, response)
}

func writeProxyError(writer http.ResponseWriter, err error) {
	status := http.StatusBadGateway
	switch {
	case errors.Is(err, memoryproxy.ErrInvalid):
		status = http.StatusBadRequest
	case errors.Is(err, memoryproxy.ErrSensitive):
		status = http.StatusUnprocessableEntity
	case errors.Is(err, memoryproxy.ErrDisabled), errors.Is(err, memoryproxy.ErrRulesUnavailable):
		status = http.StatusServiceUnavailable
	case errors.Is(err, memoryproxy.ErrInProgress):
		status = http.StatusConflict
		writer.Header().Set("Retry-After", "5")
	}
	writeMemoryError(writer, status, memoryproxy.PublicError(err))
}

func decodeLocalMemoryRequest(writer http.ResponseWriter, request *http.Request, output interface{}) bool {
	request.Body = http.MaxBytesReader(writer, request.Body, 8*1024)
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		writeMemoryError(writer, http.StatusBadRequest, "invalid_request")
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeMemoryError(writer, http.StatusBadRequest, "invalid_request")
		return false
	}
	return true
}

func allowedBrowserSource(request *http.Request) bool {
	for _, rawURL := range []string{request.Header.Get("Origin"), request.Header.Get("Referer")} {
		if strings.TrimSpace(rawURL) == "" {
			continue
		}
		parsed, err := url.Parse(rawURL)
		if err != nil || !allowedHost(parsed.Host) {
			return false
		}
	}
	return true
}

func writeMemoryError(writer http.ResponseWriter, status int, message string) {
	writeMemoryJSON(writer, status, map[string]string{"error": message})
}

func writeMemoryJSON(writer http.ResponseWriter, status int, value interface{}) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
