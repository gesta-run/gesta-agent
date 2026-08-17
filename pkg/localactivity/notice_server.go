package localactivity

import (
	"net/http"
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
		writeMemoryError(writer, http.StatusMethodNotAllowed, "method_not_allowed")
		return
	}
	if !allowedBrowserSource(request) {
		writeMemoryError(writer, http.StatusForbidden, "forbidden_origin")
		return
	}
	detail, err := h.store.Get(request.Header.Get(ActivityHeaderName))
	if err != nil {
		writeMemoryError(writer, http.StatusNotFound, "activity_not_found")
		return
	}
	notice := formatActivityNotice(detail)
	response := noticeResponse{Notice: notice}
	if notice != "" {
		response.DetailsURL = ActivityURL(detail.ActivityID)
	}
	writeMemoryJSON(writer, http.StatusOK, response)
}

func formatActivityNotice(detail activitydetail.Detail) string {
	contextCount := len(detail.ContextMatches)
	memoryValue, memoryFailed := memoryNoticeValue(detail.MemoryRecallStatus, detail.MemoryCount)
	equivalentLOC := detail.Output.EquivalentLOC()
	if contextCount == 0 && detail.MemoryCount == 0 && !memoryFailed && equivalentLOC == 0 {
		return ""
	}
	message := "Gesta · Context " + strconv.Itoa(contextCount) +
		" · Memory " + memoryValue +
		" · Last output " + formatEquivalentLOC(equivalentLOC) + " eLOC" +
		" · [Details](" + ActivityURL(detail.ActivityID) + ")"
	if utf8.RuneCountInString(message) <= maxNoticeRunes {
		return message
	}
	runes := []rune(message)
	return string(runes[:maxNoticeRunes-1]) + "…"
}

func memoryNoticeValue(status activitydetail.MemoryRecallStatus, count int) (string, bool) {
	switch status {
	case activitydetail.MemoryRecallTimeout:
		return "timeout", true
	case activitydetail.MemoryRecallError:
		return "error", true
	case activitydetail.MemoryRecallDisabled:
		return "disabled", false
	default:
		return strconv.Itoa(count), false
	}
}

func formatEquivalentLOC(value float64) string {
	formatted := strconv.FormatFloat(value, 'f', 3, 64)
	formatted = strings.TrimRight(strings.TrimRight(formatted, "0"), ".")
	if formatted == "" {
		return "0"
	}
	return formatted
}
