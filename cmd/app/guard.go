package app

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/daemon"
	"github.com/gesta-run/gesta-agent/pkg/model"
	"github.com/gesta-run/gesta-agent/pkg/policy"
	"github.com/gesta-run/gesta-agent/pkg/util"
)

const (
	GuardBlockedExitCode  = 45
	GuardApprovalExitCode = 46

	gestaHighRiskCommandDeniedMessage   = "Gesta does not allow this high-risk command to run"
	gestaHighRiskCommandApprovalMessage = "Gesta requires approval before this high-risk command can run"
)

type ExitError struct {
	Code    int
	Message string
}

func (e ExitError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("exit code %d", e.Code)
}

func guard(ctx context.Context, args []string) error {
	var agentType string
	fs := flag.NewFlagSet("guard", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&agentType, "agent", "", "agent type to evaluate")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if agentType == "" {
		return fmt.Errorf("--agent is required")
	}
	if agentType != "codex" {
		return fmt.Errorf("unsupported --agent %q; only codex is supported", agentType)
	}
	commandArgs := fs.Args()
	if len(commandArgs) == 0 {
		return fmt.Errorf("missing guarded command; usage: gesta-agent guard --agent codex -- <command...>")
	}
	cfg, shouldFlush := guardConfig()
	return runGuarded(ctx, cfg, agentType, commandArgs, shouldFlush)
}

func runGuarded(ctx context.Context, cfg daemon.Config, agentType string, commandArgs []string, shouldFlush bool) error {
	if agentType == "" {
		agentType = "codex"
	}
	if agentType != "codex" {
		return fmt.Errorf("unsupported --agent %q; only codex is supported", agentType)
	}
	if len(commandArgs) == 0 {
		return fmt.Errorf("missing guarded command")
	}

	evaluation := evaluateGuardCommandWithConfig(agentType, commandArgs, cfg, shouldFlush)
	if evaluation.Decision == policy.DecisionWarn {
		fmt.Fprintf(os.Stderr, "gesta-agent guard: warning: %s\n", evaluation.Reason)
	}
	if evaluation.Decision == policy.DecisionApproval {
		recordGuardDecisionBestEffortWithConfig(cfg, shouldFlush, evaluation, false, GuardApprovalExitCode)
		return ExitError{
			Code:    GuardApprovalExitCode,
			Message: "gesta-agent guard: " + gestaHighRiskCommandApprovalMessage,
		}
	}
	if evaluation.Decision == policy.DecisionBlock {
		recordGuardDecisionBestEffortWithConfig(cfg, shouldFlush, evaluation, false, GuardBlockedExitCode)
		return ExitError{
			Code:    GuardBlockedExitCode,
			Message: "gesta-agent guard: " + gestaHighRiskCommandDeniedMessage,
		}
	}

	exitCode, started, runErr := runGuardedCommand(ctx, commandArgs)
	recordGuardDecisionBestEffortWithConfig(cfg, shouldFlush, evaluation, started, exitCode)
	if runErr == nil {
		return nil
	}
	if started {
		return ExitError{Code: exitCode}
	}
	return ExitError{
		Code:    exitCode,
		Message: "gesta-agent guard: failed to start command: " + runErr.Error(),
	}
}

func runGuardedCommand(ctx context.Context, args []string) (int, bool, error) {
	env := os.Environ()
	executable := args[0]
	if !strings.ContainsRune(executable, rune(os.PathSeparator)) {
		resolved, err := lookPathIn(executable, os.Getenv("PATH"))
		if err != nil {
			return 127, false, err
		}
		executable = resolved
	}
	cmd := exec.CommandContext(ctx, executable, args[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = env

	if err := cmd.Start(); err != nil {
		return 127, false, err
	}
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return 130, true, err
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode := exitErr.ExitCode()
			if exitCode >= 0 {
				return exitCode, true, err
			}
			return 1, true, err
		}
		return 1, true, err
	}
	return 0, true, nil
}

func lookPathIn(file, pathValue string) (string, error) {
	if strings.ContainsRune(file, rune(os.PathSeparator)) {
		return file, nil
	}
	if pathValue == "" {
		pathValue = os.Getenv("PATH")
	}
	for _, dir := range filepath.SplitList(pathValue) {
		if dir == "" {
			dir = "."
		}
		candidate := filepath.Join(dir, file)
		if isExecutable(candidate) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("executable file not found in PATH: %s", file)
}

func isExecutable(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	return info.Mode()&0o111 != 0
}

func evaluateGuardCommand(agentType string, args []string) policy.Evaluation {
	cfg, shouldFetch := guardConfig()
	return evaluateGuardCommandWithConfig(agentType, args, cfg, shouldFetch)
}

func evaluateGuardCommandWithConfig(agentType string, args []string, cfg daemon.Config, shouldFetch bool) policy.Evaluation {
	if shouldFetch {
		client := daemon.NewClient(cfg.EffectiveServerURL(), cfg.Token)
		rules, err := client.PolicyRules()
		if err == nil {
			return policy.EvaluateCommandWithRules(agentType, args, rules)
		}
		if cached, cacheErr := daemon.LoadPolicyCache(cfg.DataDir); cacheErr == nil {
			fmt.Fprintf(os.Stderr, "gesta-agent guard: using cached policy from %s; policy sync failed: %v\n", cached.SyncedAt.Format(time.RFC3339), err)
			return policy.EvaluateCommandWithRules(agentType, args, cached.Rules)
		}
		fmt.Fprintf(os.Stderr, "gesta-agent guard: no configured policy available; policy sync failed: %v\n", err)
	}
	if cached, cacheErr := daemon.LoadPolicyCache(cfg.DataDir); cacheErr == nil {
		return policy.EvaluateCommandWithRules(agentType, args, cached.Rules)
	}
	return policy.EvaluateCommand(agentType, args)
}

func recordGuardDecisionBestEffort(evaluation policy.Evaluation, executed bool, exitCode int) {
	cfg, shouldFlush := guardConfig()
	recordGuardDecisionBestEffortWithConfig(cfg, shouldFlush, evaluation, executed, exitCode)
}

func recordGuardDecisionBestEffortWithConfig(cfg daemon.Config, shouldFlush bool, evaluation policy.Evaluation, executed bool, exitCode int) {
	if err := recordGuardDecisionWithConfig(cfg, shouldFlush, evaluation, executed, exitCode); err != nil {
		fmt.Fprintf(os.Stderr, "gesta-agent guard: policy decision was not recorded: %v\n", err)
	}
}

func recordGuardDecision(evaluation policy.Evaluation, executed bool, exitCode int) error {
	cfg, shouldFlush := guardConfig()
	return recordGuardDecisionWithConfig(cfg, shouldFlush, evaluation, executed, exitCode)
}

func recordGuardDecisionWithConfig(cfg daemon.Config, shouldFlush bool, evaluation policy.Evaluation, executed bool, exitCode int) error {
	if !evaluation.MatchedRule() {
		return nil
	}
	event := model.EventEnvelope{
		EventID:      util.NewID("evt"),
		CustomerID:   cfg.CustomerID,
		DeploymentID: cfg.DeploymentID,
		DaemonID:     cfg.DaemonID,
		DeviceID:     cfg.DeviceID,
		TeamID:       cfg.TeamID,
		EventType:    "policy.decision",
		Source:       "guard",
		AgentType:    evaluation.AgentType,
		CreatedAt:    time.Now().UTC(),
		Payload:      evaluation.Payload(executed, exitCode),
	}
	queue := daemon.NewQueue(cfg.DataDir)
	if err := queue.Append([]model.EventEnvelope{event}); err != nil {
		return fmt.Errorf("queue policy decision: %w", err)
	}
	if !shouldFlush {
		return nil
	}
	client := daemon.NewClient(cfg.EffectiveServerURL(), cfg.Token)
	if err := queue.Drain(client.SendEvents); err != nil {
		fmt.Fprintf(os.Stderr, "gesta-agent guard: queued decision locally; flush failed: %v\n", err)
		return nil
	}
	return nil
}

func guardConfig() (daemon.Config, bool) {
	cfg, err := daemon.LoadConfig("")
	if err == nil {
		return cfg, cfg.Token != "" && cfg.EffectiveServerURL() != ""
	}
	cfg = daemon.NewDirectRuntimeConfig(os.Getenv("GESTA_CONTROL_URL"), "")
	return cfg, false
}
