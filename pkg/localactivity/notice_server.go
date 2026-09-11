package localactivity

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/gesta-run/gesta-agent/pkg/activitydetail"
)

const maxNoticeRunes = 320

type noticeResponse struct {
	Notice     string `json:"notice"`
	DetailsURL string `json:"details_url,omitempty"`
}

func (h handler) serveActivityNotice(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		writeAPIError(writer, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if !allowedBrowserSource(request) {
		writeAPIError(writer, http.StatusForbidden, "forbidden_origin")
		return
	}
	detail, err := h.store.Get(request.Header.Get(ActivityHeaderName))
	if err != nil {
		writeAPIError(writer, http.StatusNotFound, "activity_not_found")
		return
	}
	notice := formatActivityNotice(detail)
	response := noticeResponse{
		Notice:     notice,
		DetailsURL: ActivityURL(detail.ActivityID),
	}
	writeAPIJSON(writer, http.StatusOK, response)
}

func formatActivityNotice(detail activitydetail.Detail) string {
	contextCount := len(detail.ContextMatches)
	equivalentLOC := detail.Output.EquivalentLOC()
	message := "Gesta · Context " + strconv.Itoa(contextCount) +
		" · Last output " + formatEquivalentLOC(equivalentLOC) + " eLOC" +
		" · [Details](" + ActivityURL(detail.ActivityID) + ")"
	if utf8.RuneCountInString(message) <= maxNoticeRunes {
		return message
	}
	runes := []rune(message)
	return string(runes[:maxNoticeRunes-1]) + "…"
}

func formatEquivalentLOC(value float64) string {
	formatted := strconv.FormatFloat(value, 'f', 3, 64)
	formatted = strings.TrimRight(strings.TrimRight(formatted, "0"), ".")
	if formatted == "" {
		return "0"
	}
	return formatted
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

func writeAPIError(writer http.ResponseWriter, status int, message string) {
	writeAPIJSON(writer, status, map[string]string{"error": message})
}

func writeAPIJSON(writer http.ResponseWriter, status int, value interface{}) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
