package agent

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/agent/options"
	"github.com/gesta-run/gesta-agent/pkg/agentupgrade"
	"github.com/gesta-run/gesta-agent/pkg/daemon"
	"github.com/gesta-run/gesta-agent/pkg/hookinstall"
	"github.com/gesta-run/gesta-agent/pkg/localactivity"
	"github.com/gesta-run/gesta-agent/pkg/model"
)

func Run(ctx context.Context, args []string) error {
	if len(args) < 1 {
		return usageError()
	}

	switch args[0] {
	case "run":
		return run(ctx, args[1:])
	case "status":
		return status(args[1:])
	case "version":
		return version(args[1:])
	case "upgrade":
		return upgrade(args[1:])
	case "guard":
		return guard(ctx, args[1:])
	case "codex-hook":
		return codexHook(ctx, args[1:])
	case "claude-hook":
		return claudeHook(ctx, args[1:])
	case "install":
		return install(args[1:])
	case "install-codex":
		return install(args[1:])
	default:
		return usageError()
	}
}

func run(ctx context.Context, args []string) error {
	opts := options.NewRunOptions()
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	opts.AddFlags(fs)
	if err := fs.Parse(args); err != nil {
		return err
	}

	commandArgs := fs.Args()
	cfg, err := configForRun(opts, true)
	if err != nil {
		return err
	}
	if err := daemon.SaveConfig("", cfg); err != nil {
		return fmt.Errorf("save daemon state: %w", err)
	}
	if len(commandArgs) > 0 {
		return runGuarded(ctx, cfg, opts.Agent, commandArgs, true)
	}
	agentPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve agent executable: %w", err)
	}
	if err := installAgentHooks(agentPath); err != nil {
		return err
	}
	runner, err := daemon.NewRunner(cfg)
	if err != nil {
		return err
	}
	localActivityServer, localActivityErr := localactivity.Start(cfg.DataDir, slog.Default())
	if localActivityErr != nil {
		slog.Warn("local activity server unavailable", "error", localActivityErr)
	} else {
		defer func() {
			shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if shutdownErr := localActivityServer.Close(shutdownCtx); shutdownErr != nil {
				slog.Warn("local activity server shutdown failed", "error", shutdownErr)
			}
		}()
	}
	if err := runner.RunLoop(ctx, opts.Interval); errors.Is(err, daemon.ErrUpgradeApplied) {
		return reexecAgent()
	} else if err != nil && err != context.Canceled {
		return err
	}
	return nil
}

func reexecAgent() error {
	agentPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve upgraded agent executable: %w", err)
	}
	return syscall.Exec(agentPath, os.Args, os.Environ())
}

func version(args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("version does not accept arguments")
	}
	fmt.Println(model.DaemonVersion)
	return nil
}

func upgrade(args []string) error {
	fs := flag.NewFlagSet("upgrade", flag.ExitOnError)
	var downloadURL string
	var sha256 string
	var checksumURL string
	var targetVersion string
	var targetPath string
	var force bool
	fs.StringVar(&downloadURL, "url", "", "gesta-agent binary URL")
	fs.StringVar(&sha256, "sha256", "", "expected binary sha256")
	fs.StringVar(&checksumURL, "checksum-url", "", "SHA256SUMS URL used when --sha256 is not set")
	fs.StringVar(&targetVersion, "target-version", "", "expected daemon version after download")
	fs.StringVar(&targetPath, "target-bin", "", "agent binary path to replace; defaults to the running executable")
	fs.BoolVar(&force, "force", false, "replace even when the target version is not newer")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if downloadURL == "" {
		return fmt.Errorf("--url is required")
	}
	if targetVersion == "" {
		return fmt.Errorf("--target-version is required")
	}
	policy := model.AgentUpgradePolicy{
		Mode:          "auto",
		TargetVersion: strings.TrimSpace(targetVersion),
		URL:           strings.TrimSpace(downloadURL),
		SHA256:        strings.TrimSpace(sha256),
		ChecksumURL:   strings.TrimSpace(checksumURL),
	}
	if !force {
		decision := agentupgrade.DecideAgentUpgrade(policy, model.DaemonVersion)
		if !decision.ShouldApply {
			uiOK("Agent upgrade skipped", decision.Reason)
			return nil
		}
	}
	if targetPath == "" {
		var err error
		targetPath, err = os.Executable()
		if err != nil {
			return fmt.Errorf("resolve agent executable: %w", err)
		}
	}
	if err := agentupgrade.ApplyAgentUpgradeToPath(context.Background(), policy, targetPath); err != nil {
		return err
	}
	uiOK("Agent upgraded", policy.TargetVersion)
	return nil
}

func install(args []string) error {
	fs := flag.NewFlagSet("install", flag.ExitOnError)
	var agentPath string
	controlURL := os.Getenv("GESTA_CONTROL_URL")
	apiKey := firstNonEmpty(os.Getenv("GESTA_API_KEY"), os.Getenv("GESTA_APIKEY"))
	var usageWindow time.Duration
	fs.StringVar(&agentPath, "agent-bin", "", "gesta-agent binary path to install into the Codex hook")
	fs.StringVar(&controlURL, "control-url", controlURL, "control plane URL to save for hook policy fetches")
	fs.StringVar(&apiKey, "apikey", apiKey, "API key to save for hook policy fetches")
	fs.DurationVar(&usageWindow, "usage-window", 10*time.Minute, "token usage accounting window saved with daemon config")
	if err := fs.Parse(args); err != nil {
		return err
	}
	controlURL = strings.TrimSpace(controlURL)
	apiKey = strings.TrimSpace(apiKey)
	if controlURL == "" {
		return fmt.Errorf("--control-url is required")
	}
	if apiKey == "" {
		return fmt.Errorf("--apikey is required")
	}
	if agentPath == "" {
		var err error
		agentPath, err = os.Executable()
		if err != nil {
			return fmt.Errorf("resolve agent executable: %w", err)
		}
	}
	if err := installAgentHooks(agentPath); err != nil {
		return err
	}
	cfg := daemon.NewDirectRuntimeConfig(controlURL, apiKey)
	if usageWindow > 0 {
		cfg.UsageWindow = usageWindow.String()
	}
	if err := daemon.SaveConfig("", cfg); err != nil {
		return fmt.Errorf("save daemon state: %w", err)
	}
	uiOK("Daemon config saved", controlURL)
	return nil
}

func installAgentHooks(agentPath string) error {
	hookPath, err := hookinstall.InstallCodexPolicyHook(agentPath)
	if err != nil {
		return fmt.Errorf("install Codex policy hook: %w", err)
	}
	uiOK("Codex policy hook installed and trusted", hookPath)
	if disabled, path := hookinstall.CodexHooksDisabled(); disabled {
		uiWarn(fmt.Sprintf("Codex hooks are disabled in %s; set [features].hooks=true and restart Codex to enable policy checks and turn output measurement", path))
	}

	claudeSettingsPath, err := hookinstall.InstallClaudeCodePolicyHook(agentPath)
	if err != nil {
		return fmt.Errorf("install Claude Code policy hook: %w", err)
	}
	uiOK("Claude Code policy hook installed", claudeSettingsPath)
	return nil
}

func status(args []string) error {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := daemon.LoadConfig("")
	if err != nil {
		return fmt.Errorf("load daemon state: %w\nnote: run install first or pass --apikey when starting the daemon", err)
	}
	fmt.Print(statusOutput(cfg))
	return nil
}

func statusOutput(cfg daemon.Config) string {
	auth := "not_configured"
	if strings.TrimSpace(cfg.APIKey) != "" {
		auth = "configured"
	}
	return fmt.Sprintf("version=%s\nstate_path=%s\ndaemon_id=%s\nauth=%s\nserver_url=%s\npolicy_version=%s\ndata_dir=%s\n",
		model.DaemonVersion, daemon.DefaultStatePath(), cfg.DaemonID, auth, cfg.EffectiveServerURL(), cfg.PolicyVersion, cfg.DataDir)
}

func configForRun(opts options.RunOptions, allowSaved bool) (daemon.Config, error) {
	apiKey := opts.APIKey
	if apiKey == "" {
		apiKey = opts.Token
	}
	if apiKey == "" {
		if allowSaved {
			cfg, err := daemon.LoadConfig("")
			if err == nil {
				if opts.ControlURL != "" {
					cfg.ServerURL = opts.ControlURL
					cfg.ControlURL = opts.ControlURL
				}
				if opts.UsageWindow > 0 {
					cfg.UsageWindow = opts.UsageWindow.String()
				}
				return cfg, nil
			}
		}
		return daemon.Config{}, fmt.Errorf("--apikey is required")
	}
	cfg := daemon.NewDirectRuntimeConfig(opts.ControlURL, apiKey)
	if opts.UsageWindow > 0 {
		cfg.UsageWindow = opts.UsageWindow.String()
	}
	return cfg, nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func usageError() error {
	return fmt.Errorf(`usage: gesta-agent

Commands:
  run --control-url http://localhost:8080 --apikey user_api_key
  run --control-url http://localhost:8080 --apikey user_api_key -- <command...>
  install [--agent-bin /path/to/gesta-agent] --control-url http://localhost:8080 --apikey user_api_key
  upgrade --url https://.../gesta-agent-darwin-arm64 --target-version 0.0.1-rc22 --checksum-url https://.../SHA256SUMS
  status
  version
  guard --agent codex -- <command...>

Internal commands:
  codex-hook    invoked by the Codex hook integration
  claude-hook   invoked by the Claude Code hook integration

Environment:
  GESTA_CONTROL_URL  control plane URL used by run/install
  GESTA_API_KEY      API key used by install`)
}
