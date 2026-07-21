package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/model"
	"github.com/gesta-run/gesta-agent/pkg/privacy"
	"github.com/gesta-run/gesta-agent/pkg/util"
)

const (
	codexMaxTranscriptMessages     = 80
	codexMaxTranscriptMessageBytes = 8 * 1024
	codexMaxTranscriptTotalBytes   = 256 * 1024
	codexMaxTranscriptRows         = 40
	codexTranscriptTailBytes       = 8 * 1024 * 1024
	codexSensitiveTranscriptWindow = time.Hour
)

type CodexAdapter struct{}

func (CodexAdapter) Name() string { return "codex" }

func (a CodexAdapter) Collect(ctx context.Context, cfg Config) (AdapterResult, []model.EventEnvelope) {
	now := time.Now().UTC().Format(time.RFC3339)
	path := codexBinaryPath()
	stateDB := latestCodexStateDB()
	if path == "" && stateDB == "" {
		return AdapterResult{Status: model.AdapterStatus{Name: a.Name(), Detected: false, Status: "not_found", UpdatedAt: now}}, nil
	}
	statusText := "ok"
	version := ""
	if path != "" {
		version = safeVersion(ctx, path, "--version")
	} else {
		statusText = "state_db_only"
	}
	status := model.AdapterStatus{Name: a.Name(), Detected: true, Version: version, Status: statusText, UpdatedAt: now}

	var events []model.EventEnvelope
	discovery := map[string]interface{}{
		"version": version,
	}
	if path != "" {
		discovery["binary_path_hash"] = util.ShortHash(path)
		discovery["binary_found"] = true
	} else {
		discovery["binary_found"] = false
		discovery["state_db_hash"] = util.ShortHash(stateDB)
	}
	events = append(events, snapshotEvent(cfg, "agent.discovery", "daemon", "codex", discovery))

	if path != "" {
		mcpOutput, err := commandOutput(ctx, path, "mcp", "list")
		if err == nil {
			payload := commandOutputMetadata(mcpOutput)
			servers := mcpServersFromListOutput(mcpOutput)
			payload["command"] = "codex mcp list"
			payload["servers"] = servers
			payload["server_count"] = len(servers)
			events = append(events, snapshotEvent(cfg, "mcp.inventory", "codex", "codex", payload))
		}
	}

	if stateDB != "" {
		if aggregate, usageEvents, transcriptEvents, err := readCodexState(ctx, stateDB); err == nil {
			// Register each session's repo for the merged-commit scan
			// (CLO-1747) BEFORE the backfill filter: the hook pipeline cannot
			// be relied on for codex, and on the first collection after an
			// install or upgrade the filter drops every pre-existing session —
			// exactly the sessions whose repos need discovering. Discovery
			// only writes the local registry, so backfill semantics are not
			// affected.
			for _, transcript := range transcriptEvents {
				if cwd := firstString(transcript, "_cwd"); cwd != "" {
					codexRepoDiscovery.observeCwd(ctx, cfg.DataDir, cwd)
				} else {
					codexRepoDiscovery.observeRollout(ctx, cfg.DataDir, firstString(transcript, "_rollout_path"))
				}
			}
			sensitiveRules := codexSensitiveRulesForCollection(cfg)
			sensitiveEvents := codexSensitiveFindingEventsFromTranscripts(cfg, transcriptEvents, sensitiveRules)
			filteredUsage, filteredTranscripts, baselineMeta, err := filterCodexSessionBackfill(cfg, stateDB, usageEvents, transcriptEvents, time.Now().UTC())
			for key, value := range baselineMeta {
				aggregate[key] = value
			}
			if err != nil {
				events = append(events, snapshotEvent(cfg, "adapter.warning", "codex", "codex", map[string]interface{}{
					"state_db_hash": util.ShortHash(stateDB),
					"error":         privacy.RedactAndTruncate(err.Error(), 2048),
					"scope":         "session_baseline",
				}))
				usageEvents = nil
				transcriptEvents = nil
			} else {
				usageEvents = filteredUsage
				transcriptEvents = filteredTranscripts
			}
			events = append(events, snapshotEvent(cfg, "codex.usage_summary", "codex", "codex", aggregate))
			for _, usage := range usageEvents {
				events = append(events, baseEvent(cfg, "usage.summary", "codex", "codex", usage))
			}
			for _, transcript := range transcriptEvents {
				events = append(events, codexToolCallEventsFromTranscript(cfg, transcript)...)
				if outputEvent, ok := codexOutputSummaryEvent(ctx, cfg, transcript); ok {
					events = append(events, outputEvent)
				}
				publicTranscript := codexPublicTranscriptPayload(transcript)
				event := baseEvent(cfg, "session.transcript", "codex", "codex", publicTranscript)
				event.EventID = codexTranscriptEventID(publicTranscript)
				events = append(events, event)
			}
			events = append(events, sensitiveEvents...)
		} else {
			events = append(events, snapshotEvent(cfg, "adapter.warning", "codex", "codex", map[string]interface{}{
				"state_db_hash": util.ShortHash(stateDB),
				"error":         privacy.RedactAndTruncate(err.Error(), 2048),
			}))
		}
	}
	return AdapterResult{Status: status}, events
}

func codexBinaryPath() string {
	return codexBinaryPathWithCandidates(defaultCodexBinaryCandidates())
}

func codexBinaryPathWithCandidates(candidates []string) string {
	if path, err := exec.LookPath("codex"); err == nil && path != "" {
		return path
	}
	return firstExecutablePath(candidates)
}

func defaultCodexBinaryCandidates() []string {
	home, _ := os.UserHomeDir()
	candidates := []string{
		"/Applications/ChatGPT.app/Contents/Resources/codex",
		"/Applications/Codex.app/Contents/Resources/codex",
	}
	if home != "" {
		candidates = append(candidates,
			filepath.Join(home, "Applications", "ChatGPT.app", "Contents", "Resources", "codex"),
			filepath.Join(home, "Applications", "Codex.app", "Contents", "Resources", "codex"),
		)
	}
	return candidates
}

func firstExecutablePath(candidates []string) string {
	for _, candidate := range candidates {
		if isExecutableFile(candidate) {
			return candidate
		}
	}
	return ""
}

func isExecutableFile(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return info.Mode().Perm()&0o111 != 0
}

func safeVersion(ctx context.Context, name string, args ...string) string {
	out, err := commandOutput(ctx, name, args...)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(privacy.RedactAndTruncate(out, 512))
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func stringInt(value int64) string {
	return strconv.FormatInt(value, 10)
}

func isSafeSourceLabel(source string) bool {
	switch source {
	case "cli", "vscode", "app", "cloud", "ci":
		return true
	default:
		return false
	}
}

func firstString(row map[string]interface{}, names ...string) string {
	for _, name := range names {
		if value, ok := row[name].(string); ok && value != "" {
			return value
		}
	}
	return ""
}

func copyStringField(dst, src map[string]interface{}, name string) {
	if value := firstString(src, name); value != "" {
		dst[name] = value
	}
}

func copyCodexTimeField(dst, src map[string]interface{}, name string) {
	if value := firstString(src, name); value != "" {
		dst[name] = value
		return
	}
	if value, ok := asInt64(src[name+"_ms"]); ok && value > 0 {
		dst[name] = time.UnixMilli(value).UTC().Format(time.RFC3339Nano)
		return
	}
	if value, ok := asInt64(src[name]); ok && value > 0 {
		dst[name] = time.Unix(value, 0).UTC().Format(time.RFC3339Nano)
	}
}

func copyNumberField(dst, src map[string]interface{}, name string) {
	if value, ok := asInt64(src[name]); ok {
		dst[name] = value
	}
}

func totalTokens(row map[string]interface{}) int64 {
	if value, ok := asInt64(row["tokens_used"]); ok {
		return value
	}
	if value, ok := asInt64(row["total_tokens"]); ok {
		return value
	}
	input, _ := asInt64(row["input_tokens"])
	output, _ := asInt64(row["output_tokens"])
	return input + output
}

func asInt64(value interface{}) (int64, bool) {
	switch typed := value.(type) {
	case int:
		return int64(typed), true
	case int64:
		return typed, true
	case float64:
		return int64(typed), true
	case json.Number:
		parsed, err := typed.Int64()
		return parsed, err == nil
	default:
		return 0, false
	}
}

func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}

func sqliteJSON(ctx context.Context, dbPath, sql string) ([]map[string]interface{}, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cmdCtx, "sqlite3", "-readonly", "-json", dbPath, sql)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, privacy.RedactAndTruncate(strings.TrimSpace(string(out)), 2048))
	}
	var rows []map[string]interface{}
	if err := json.Unmarshal(out, &rows); err != nil {
		return nil, err
	}
	return rows, nil
}
