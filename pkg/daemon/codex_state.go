package daemon

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	turnusage "github.com/gesta-run/gesta-agent/pkg/turn"
	"github.com/gesta-run/gesta-agent/pkg/util"
)

func latestCodexStateDB() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	matches := findFiles(filepath.Join(home, ".codex", "state_*.sqlite"))
	if len(matches) == 0 {
		return ""
	}
	sort.Slice(matches, func(i, j int) bool {
		iInfo, iErr := os.Stat(matches[i])
		jInfo, jErr := os.Stat(matches[j])
		if iErr != nil || jErr != nil {
			return matches[i] > matches[j]
		}
		return iInfo.ModTime().After(jInfo.ModTime())
	})
	return matches[0]
}

func readCodexState(ctx context.Context, dbPath string) (map[string]interface{}, []map[string]interface{}, []map[string]interface{}, []turnusage.CodexSession, error) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		return map[string]interface{}{
			"state_db_present": true,
			"state_db_hash":    util.ShortHash(dbPath),
			"sqlite3_present":  false,
		}, nil, nil, nil, nil
	}
	columns, err := sqliteColumns(ctx, dbPath, "threads")
	if err != nil {
		return nil, nil, nil, nil, err
	}
	aggregateSQL := codexAggregateSQL(columns)
	aggregateRows, err := sqliteJSON(ctx, dbPath, aggregateSQL)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	aggregate := map[string]interface{}{
		"state_db_present": true,
		"state_db_hash":    util.ShortHash(dbPath),
		"sqlite3_present":  true,
		"threads":          0,
		"total_tokens":     0,
		"metadata_only":    true,
	}
	if len(aggregateRows) > 0 {
		for key, value := range aggregateRows[0] {
			aggregate[key] = value
		}
		if sessions, ok := asInt64(aggregateRows[0]["session_count"]); ok {
			aggregate["threads"] = sessions
		}
		aggregate["total_tokens"] = totalTokens(aggregateRows[0])
	}

	usageSQL := codexUsageSQL(columns)
	if usageSQL == "" {
		return aggregate, nil, nil, nil, nil
	}
	usageRows, err := sqliteJSON(ctx, dbPath, usageSQL)
	if err != nil {
		return aggregate, nil, nil, nil, err
	}
	spawnParents := codexThreadSpawnParents(ctx, dbPath)
	sessionTitles := codexSessionIndexTitles(codexSessionIndexPath())
	usage, transcripts, turnSessions := collectCodexRows(usageRows, spawnParents, sessionTitles)
	return aggregate, usage, transcripts, turnSessions, nil
}

func collectCodexRows(usageRows []map[string]interface{}, spawnParents, sessionTitles map[string]string) ([]map[string]interface{}, []map[string]interface{}, []turnusage.CodexSession) {
	usage := make([]map[string]interface{}, 0, len(usageRows))
	transcripts := make([]map[string]interface{}, 0, len(usageRows))
	turnSessions := make([]turnusage.CodexSession, 0, len(usageRows))
	for index, row := range usageRows {
		if sessionID := firstString(row, "session_id", "id"); sessionID != "" {
			if parentID := spawnParents[sessionID]; parentID != "" {
				row["parent_session_id"] = parentID
			}
		}
		payload := codexUsagePayload(row, sessionTitles)
		if len(payload) > 0 {
			usage = append(usage, payload)
		}
		if session, ok := codexTurnSession(row, payload); ok {
			turnSessions = append(turnSessions, session)
		}
		if index >= codexMaxTranscriptRows {
			continue
		}
		if transcript := codexTranscriptPayload(row, payload, sessionTitles); len(transcript) > 0 {
			transcripts = append(transcripts, transcript)
		}
	}
	return usage, transcripts, turnSessions
}

func codexTurnSession(row, usagePayload map[string]interface{}) (turnusage.CodexSession, bool) {
	session := turnusage.CodexSession{
		SessionID:       firstString(usagePayload, "session_id_hash", "session_id"),
		ParentSessionID: firstString(usagePayload, "parent_session_id_hash", "parent_session_id"),
		RolloutPath:     firstString(row, "rollout_path"),
		Model:           firstString(row, "model", "model_name", "model_id"),
		Repo:            firstString(usagePayload, "repo", "repo_path_hash", "cwd_hash", "source_hash", "workspace_hash"),
		ModelProvider:   firstString(row, "model_provider"),
	}
	return session, session.SessionID != "" && session.RolloutPath != ""
}

func codexThreadSpawnParents(ctx context.Context, dbPath string) map[string]string {
	rows, err := sqliteJSON(ctx, dbPath, `select parent_thread_id, child_thread_id from thread_spawn_edges where parent_thread_id != '' and child_thread_id != '';`)
	if err != nil {
		return nil
	}
	parents := map[string]string{}
	for _, row := range rows {
		parentID := firstString(row, "parent_thread_id")
		childID := firstString(row, "child_thread_id")
		if parentID == "" || childID == "" {
			continue
		}
		parents[childID] = parentID
	}
	return parents
}

func sqliteColumns(ctx context.Context, dbPath, table string) (map[string]bool, error) {
	rows, err := sqliteJSON(ctx, dbPath, "pragma table_info("+quoteIdent(table)+");")
	if err != nil {
		return nil, err
	}
	columns := map[string]bool{}
	for _, row := range rows {
		if name, ok := row["name"].(string); ok {
			columns[name] = true
		}
	}
	return columns, nil
}

func codexAggregateSQL(columns map[string]bool) string {
	parts := []string{"count(*) as \"session_count\""}
	for _, column := range codexTokenColumns() {
		if columns[column] {
			parts = append(parts, "coalesce(sum("+quoteIdent(column)+"),0) as "+quoteIdent(column))
		}
	}
	return "select " + strings.Join(parts, ", ") + " from threads;"
}

func codexUsageSQL(columns map[string]bool) string {
	var selected []string
	for _, column := range codexRecentColumns() {
		if columns[column] {
			selected = append(selected, quoteIdent(column)+" as "+quoteIdent(column))
		}
	}
	if len(selected) == 0 {
		return ""
	}
	var tokenPredicates []string
	for _, column := range codexTokenColumns() {
		if columns[column] {
			tokenPredicates = append(tokenPredicates, "coalesce("+quoteIdent(column)+",0) > 0")
		}
	}
	if len(tokenPredicates) == 0 {
		return ""
	}
	orderBy := firstAvailableColumn(columns, "updated_at", "created_at", "id", "session_id")
	orderClause := ""
	if orderBy != "" {
		orderClause = " order by " + quoteIdent(orderBy) + " desc"
	}
	return "select " + strings.Join(selected, ", ") + " from threads where (" + strings.Join(tokenPredicates, " or ") + ")" + orderClause + ";"
}

func codexRecentColumns() []string {
	return []string{
		"id",
		"session_id",
		"source",
		"cwd",
		"repo_id",
		"repository_id",
		"repo_root",
		"worktree",
		"title",
		"session_title",
		"conversation_title",
		"thread_title",
		"parent_session_id",
		"parent_thread_id",
		"forked_from_id",
		"forked_from",
		"model_provider",
		"model",
		"rollout_path",
		"tokens_used",
		"total_tokens",
		"input_tokens",
		"output_tokens",
		"cached_input_tokens",
		"reasoning_tokens",
		"git_branch",
		"git_sha",
		"created_at",
		"created_at_ms",
		"updated_at",
		"updated_at_ms",
	}
}

func codexTokenColumns() []string {
	return []string{
		"tokens_used",
		"total_tokens",
		"input_tokens",
		"output_tokens",
		"cached_input_tokens",
		"reasoning_tokens",
	}
}

func firstAvailableColumn(columns map[string]bool, names ...string) string {
	for _, name := range names {
		if columns[name] {
			return name
		}
	}
	return ""
}

func codexUsagePayload(row map[string]interface{}, sessionTitles map[string]string) map[string]interface{} {
	payload := map[string]interface{}{
		"agent_type":    "codex",
		"metadata_only": true,
	}
	if sessionID := firstString(row, "session_id", "id"); sessionID != "" {
		hashed := util.ShortHash(sessionID)
		payload["session_id"] = hashed
		payload["session_id_hash"] = hashed
		payload["session_id_is_hashed"] = true
	}
	copyCodexParentSessionField(payload, row)
	if repoID := firstString(row, "repo_id", "repository_id"); repoID != "" {
		hashed := util.ShortHash(repoID)
		payload["repo_id"] = hashed
		payload["repo_id_hash"] = hashed
		payload["repo_id_is_hashed"] = true
	}
	if cwd := firstString(row, "cwd", "repo_root", "worktree"); cwd != "" {
		payload["cwd_hash"] = util.ShortHash(cwd)
	}
	if source := firstString(row, "source"); source != "" {
		payload["source_hash"] = util.ShortHash(source)
		if isSafeSourceLabel(source) {
			payload["source"] = source
		} else if _, ok := payload["cwd_hash"]; !ok {
			payload["cwd_hash"] = util.ShortHash(source)
		}
	}
	copyStringField(payload, row, "model_provider")
	copyStringField(payload, row, "model")
	copyStringField(payload, row, "title")
	copyStringField(payload, row, "session_title")
	copyStringField(payload, row, "conversation_title")
	copyStringField(payload, row, "thread_title")
	if title := codexSessionIndexTitle(row, sessionTitles); title != "" {
		payload["title"] = title
		payload["title_source"] = "codex_session_index"
	}
	copyStringField(payload, row, "git_branch")
	copyStringField(payload, row, "git_sha")
	copyCodexTimeField(payload, row, "created_at")
	copyCodexTimeField(payload, row, "updated_at")
	for _, column := range codexTokenColumns() {
		if column == "tokens_used" {
			continue
		}
		copyNumberField(payload, row, column)
	}
	rawTotalTokens := totalTokens(row)
	effectiveTokens := rawTotalTokens
	if rolloutPath := firstString(row, "rollout_path"); rolloutPath != "" {
		if usage, ok := codexEffectiveTokenUsage(rolloutPath); ok {
			effectiveTokens = usage.TotalTokens
			payload["token_accounting"] = "raw_total"
			payload["input_tokens"] = usage.InputTokens
			payload["cached_input_tokens"] = usage.CachedInputTokens
			payload["output_tokens"] = usage.OutputTokens
			payload["effective_tokens"] = usage.EffectiveTokens()
			payload["raw_total_tokens"] = usage.TotalTokens
		}
	}
	payload["tokens_used"] = effectiveTokens
	payload["total_tokens"] = effectiveTokens
	return payload
}

func copyCodexParentSessionField(payload, row map[string]interface{}) {
	parentID := firstString(row, "parent_session_id", "parent_thread_id", "forked_from_id", "forked_from")
	if parentID == "" {
		parentID = codexForkParentFromRollout(firstString(row, "rollout_path"))
	}
	if parentID == "" {
		return
	}
	hashed := util.ShortHash(parentID)
	payload["parent_session_id"] = hashed
	payload["parent_session_id_hash"] = hashed
	payload["parent_session_id_is_hashed"] = true
}
