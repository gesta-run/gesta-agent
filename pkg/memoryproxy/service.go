package memoryproxy

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/controlclient"
	"github.com/gesta-run/gesta-agent/pkg/daemon"
	"github.com/gesta-run/gesta-agent/pkg/model"
	"github.com/gesta-run/gesta-agent/pkg/privacy"
	"github.com/gesta-run/gesta-agent/pkg/rulecache"
	"github.com/gesta-run/gesta-agent/pkg/util"
)

var (
	ErrDisabled         = errors.New("memory_disabled")
	ErrSensitive        = errors.New("memory request contains sensitive information")
	ErrRulesUnavailable = errors.New("sensitive rules unavailable")
	ErrInvalid          = errors.New("invalid memory request")
	ErrInProgress       = errors.New("memory write is already in progress")
)

const (
	contextRequestTimeout  = 6500 * time.Millisecond
	searchRequestTimeout   = 5500 * time.Millisecond
	rememberRequestTimeout = 185 * time.Second
)

type Service struct {
	config daemon.Config
	client *controlclient.Client
}

func New(config daemon.Config) *Service {
	return &Service{
		config: config,
		client: controlclient.NewClient(config.EffectiveServerURL(), config.Token),
	}
}

func (s *Service) Enabled() bool {
	if s == nil || strings.TrimSpace(s.config.EffectiveServerURL()) == "" ||
		strings.TrimSpace(s.config.Token) == "" || strings.TrimSpace(s.config.DaemonID) == "" {
		return false
	}
	settings, err := rulecache.LoadMemorySettingsCache(s.config.DataDir)
	return err == nil && settings.Enabled
}

func (s *Service) Context(parent context.Context, prompt, suppliedContext string, workspace model.MemoryWorkspace) (model.MemorySearchResponse, error) {
	if err := s.authorizeText(prompt); err != nil {
		return model.MemorySearchResponse{}, err
	}
	ctx, cancel := context.WithTimeout(parent, contextRequestTimeout)
	defer cancel()
	return s.client.MemoryContext(ctx, model.MemoryContextRequest{
		DaemonID: s.config.DaemonID, Prompt: strings.TrimSpace(prompt),
		Context: strings.TrimSpace(suppliedContext), Workspace: workspace,
	})
}

func (s *Service) Search(parent context.Context, query string, limit int, workspace model.MemoryWorkspace) (model.MemorySearchResponse, error) {
	query = strings.TrimSpace(query)
	if query == "" || len(query) > 2048 {
		return model.MemorySearchResponse{}, ErrInvalid
	}
	if err := s.authorizeText(query); err != nil {
		return model.MemorySearchResponse{}, err
	}
	ctx, cancel := context.WithTimeout(parent, searchRequestTimeout)
	defer cancel()
	return s.client.SearchMemory(ctx, model.MemorySearchRequest{
		DaemonID: s.config.DaemonID, Query: query, Limit: limit, Workspace: workspace,
	})
}

func (s *Service) Remember(parent context.Context, content string, workspace model.MemoryWorkspace) (model.MemoryRememberResponse, error) {
	content = strings.TrimSpace(content)
	if content == "" || len(content) > 4096 {
		return model.MemoryRememberResponse{}, ErrInvalid
	}
	if err := s.authorizeText(content); err != nil {
		return model.MemoryRememberResponse{}, err
	}
	ctx, cancel := context.WithTimeout(parent, rememberRequestTimeout)
	defer cancel()
	response, err := s.client.RememberMemory(ctx, model.MemoryRememberRequest{
		RequestID: memoryRequestID(s.config.DaemonID, content, workspace),
		DaemonID:  s.config.DaemonID,
		Content:   content, OccurredAt: time.Now().UTC(), Workspace: workspace,
	})
	if status, ok := controlclient.StatusCode(err); ok && status == 409 {
		return model.MemoryRememberResponse{}, ErrInProgress
	}
	return response, err
}

func memoryRequestID(daemonID, content string, workspace model.MemoryWorkspace) string {
	content = strings.ReplaceAll(strings.TrimSpace(content), "\r\n", "\n")
	content = strings.ReplaceAll(content, "\r", "\n")
	childDirs := make([]string, 0, len(workspace.ChildDirs))
	seenChildDirs := map[string]struct{}{}
	for _, value := range workspace.ChildDirs {
		value = strings.ToLower(strings.TrimSpace(value))
		if value == "" {
			continue
		}
		if _, exists := seenChildDirs[value]; !exists {
			seenChildDirs[value] = struct{}{}
			childDirs = append(childDirs, value)
		}
	}
	sort.Strings(childDirs)
	material := strings.Join([]string{
		strings.TrimSpace(daemonID),
		content,
		strings.ToLower(strings.TrimSpace(workspace.CWDName)),
		strings.Join(childDirs, "\x00"),
	}, "\x00")
	return "memreq_" + util.HashString(material)
}

func (s *Service) authorizeText(text string) error {
	if !s.Enabled() {
		return ErrDisabled
	}
	cache, err := rulecache.LoadSensitiveRuleCache(s.config.DataDir)
	if err != nil {
		return ErrRulesUnavailable
	}
	findings := privacy.DetectSensitiveTextWithRules(text, util.HashString(s.config.Token), cache.Rules)
	if len(findings) > 0 {
		return ErrSensitive
	}
	return nil
}

func PublicError(err error) string {
	switch {
	case errors.Is(err, ErrDisabled):
		return "memory_disabled"
	case errors.Is(err, ErrSensitive):
		return "sensitive_information"
	case errors.Is(err, ErrRulesUnavailable):
		return "sensitive_rules_unavailable"
	case errors.Is(err, ErrInvalid):
		return "invalid_request"
	case errors.Is(err, ErrInProgress):
		return "memory_write_in_progress"
	default:
		return "memory_unavailable"
	}
}
