package agentupgrade

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gesta-run/gesta-agent/pkg/atomicfile"
	"github.com/gesta-run/gesta-agent/pkg/model"
)

const upgradeHTTPTimeout = 2 * time.Minute

type UpgradeState struct {
	Enabled         bool      `json:"enabled"`
	TargetVersion   string    `json:"target_version,omitempty"`
	State           string    `json:"state"`
	LastCheckedAt   time.Time `json:"last_checked_at,omitempty"`
	LastAttemptAt   time.Time `json:"last_attempt_at,omitempty"`
	LastSucceededAt time.Time `json:"last_succeeded_at,omitempty"`
	Error           string    `json:"error,omitempty"`
}

type UpgradeDecision struct {
	Mode        string
	ShouldApply bool
	Reason      string
}

func DecideAgentUpgrade(policy model.AgentUpgradePolicy, currentVersion string) UpgradeDecision {
	mode := normalizeUpgradeMode(policy.Mode)
	if mode == "off" {
		return UpgradeDecision{Mode: "off", Reason: "server disabled upgrades"}
	}
	targetVersion := strings.TrimSpace(policy.TargetVersion)
	if targetVersion == "" {
		return UpgradeDecision{Mode: mode, Reason: "missing target version"}
	}
	if strings.TrimSpace(policy.URL) == "" {
		return UpgradeDecision{Mode: mode, Reason: "missing download url"}
	}
	cmp, ok := compareDaemonVersions(currentVersion, targetVersion)
	if !ok {
		return UpgradeDecision{Mode: mode, Reason: "could not compare daemon versions"}
	}
	if cmp >= 0 {
		return UpgradeDecision{Mode: mode, Reason: "current version is already at or above target"}
	}
	return UpgradeDecision{
		Mode:        mode,
		ShouldApply: mode == "auto" || mode == "required",
		Reason:      "target version is newer",
	}
}

func ApplyAgentUpgrade(policy model.AgentUpgradePolicy) error {
	targetPath, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve executable: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(targetPath); err == nil && resolved != "" {
		targetPath = resolved
	}
	return ApplyAgentUpgradeToPath(context.Background(), policy, targetPath)
}

func ApplyAgentUpgradeToPath(ctx context.Context, policy model.AgentUpgradePolicy, targetPath string) error {
	if !AutomaticUpgradeSupported() {
		return errors.New("automatic upgrades are not supported on Windows RC; rerun the current Connect command")
	}
	targetPath = strings.TrimSpace(targetPath)
	if targetPath == "" {
		return errors.New("target path is required")
	}
	downloadURL := strings.TrimSpace(policy.URL)
	if err := validateUpgradeURL(downloadURL); err != nil {
		return err
	}
	targetVersion := strings.TrimSpace(policy.TargetVersion)
	if targetVersion == "" {
		return errors.New("target version is required")
	}
	expectedSHA, err := expectedUpgradeSHA(ctx, policy)
	if err != nil {
		return err
	}

	dir := filepath.Dir(targetPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".gesta-agent-upgrade-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	defer os.Remove(tmpPath)

	if err := downloadUpgradeFile(ctx, downloadURL, tmpPath); err != nil {
		return err
	}
	if err := verifyFileSHA256(tmpPath, expectedSHA); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		return err
	}
	if err := verifyDownloadedAgentVersion(ctx, tmpPath, targetVersion); err != nil {
		return err
	}
	return replaceAgentBinary(tmpPath, targetPath)
}

func expectedUpgradeSHA(ctx context.Context, policy model.AgentUpgradePolicy) (string, error) {
	if sha := normalizeSHA256(policy.SHA256); sha != "" {
		return sha, nil
	}
	checksumURL := strings.TrimSpace(policy.ChecksumURL)
	if checksumURL == "" {
		return "", errors.New("sha256 or checksum_url is required")
	}
	if err := validateUpgradeURL(checksumURL); err != nil {
		return "", fmt.Errorf("checksum url: %w", err)
	}
	name := checksumAssetName(policy.URL)
	if name == "" {
		return "", errors.New("could not determine checksum asset name")
	}
	return downloadExpectedSHA(ctx, checksumURL, name)
}

func downloadExpectedSHA(ctx context.Context, checksumURL, assetName string) (string, error) {
	reqCtx, cancel := context.WithTimeout(ctx, upgradeHTTPTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, checksumURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", fmt.Errorf("download checksums: %s", resp.Status)
	}
	scanner := bufio.NewScanner(resp.Body)
	baseName := filepath.Base(assetName)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		name := strings.TrimPrefix(fields[1], "*")
		if name == assetName || filepath.Base(name) == baseName {
			sha := normalizeSHA256(fields[0])
			if sha == "" {
				return "", fmt.Errorf("checksum entry for %s is not a sha256", assetName)
			}
			return sha, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	return "", fmt.Errorf("checksum entry missing for %s", assetName)
}

func downloadUpgradeFile(ctx context.Context, rawURL, path string) error {
	reqCtx, cancel := context.WithTimeout(ctx, upgradeHTTPTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("download agent: %s", resp.Status)
	}
	out, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, resp.Body); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func verifyFileSHA256(path, expected string) error {
	expected = normalizeSHA256(expected)
	if expected == "" {
		return errors.New("expected sha256 is required")
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	sum := sha256.New()
	if _, err := io.Copy(sum, file); err != nil {
		return err
	}
	actual := hex.EncodeToString(sum.Sum(nil))
	if actual != expected {
		return fmt.Errorf("checksum mismatch: expected %s, got %s", expected, actual)
	}
	return nil
}

func verifyDownloadedAgentVersion(ctx context.Context, path, targetVersion string) error {
	reqCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(reqCtx, path, "version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("verify downloaded version: %w: %s", err, strings.TrimSpace(string(out)))
	}
	version := strings.TrimSpace(string(out))
	if version != targetVersion {
		return fmt.Errorf("downloaded version = %q, want %q", version, targetVersion)
	}
	return nil
}

func LoadUpgradeState(path string) (UpgradeState, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return UpgradeState{}, nil
	}
	if err != nil {
		return UpgradeState{}, err
	}
	var state UpgradeState
	if err := json.Unmarshal(data, &state); err != nil {
		return UpgradeState{}, err
	}
	return state, nil
}

func SaveUpgradeState(path string, state UpgradeState) error {
	return atomicfile.WriteJSON(path, state)
}

func normalizeUpgradeMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "notify", "manual":
		return "notify"
	case "auto", "automatic":
		return "auto"
	case "required", "force", "forced":
		return "required"
	default:
		return "off"
	}
}

func AutoUpdateDisabled() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("GESTA_AGENT_AUTO_UPDATE"))) {
	case "0", "false", "no", "n", "off", "disabled":
		return true
	default:
		return false
	}
}

func validateUpgradeURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return err
	}
	switch parsed.Scheme {
	case "https":
		return nil
	case "http":
		if isLocalUpgradeHost(parsed.Hostname()) {
			return nil
		}
	}
	return fmt.Errorf("upgrade URL must use https or localhost http: %s", raw)
}

func isLocalUpgradeHost(host string) bool {
	host = strings.ToLower(strings.TrimSpace(host))
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func normalizeSHA256(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) != sha256.Size*2 {
		return ""
	}
	if _, err := hex.DecodeString(value); err != nil {
		return ""
	}
	return value
}

func checksumAssetName(rawURL string) string {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	path := strings.TrimPrefix(parsed.EscapedPath(), "/")
	path, _ = url.PathUnescape(path)
	if idx := strings.Index(path, "bin/gesta-agent-"); idx >= 0 {
		return path[idx:]
	}
	return filepath.Base(path)
}

type daemonVersion struct {
	major  int
	minor  int
	patch  int
	pre    string
	preN   int
	hasPre bool
}

func compareDaemonVersions(left, right string) (int, bool) {
	lv, ok := parseDaemonVersion(left)
	if !ok {
		return 0, false
	}
	rv, ok := parseDaemonVersion(right)
	if !ok {
		return 0, false
	}
	for _, pair := range [][2]int{{lv.major, rv.major}, {lv.minor, rv.minor}, {lv.patch, rv.patch}} {
		if pair[0] < pair[1] {
			return -1, true
		}
		if pair[0] > pair[1] {
			return 1, true
		}
	}
	if lv.hasPre != rv.hasPre {
		if lv.hasPre {
			return -1, true
		}
		return 1, true
	}
	if lv.pre != rv.pre {
		if lv.pre < rv.pre {
			return -1, true
		}
		return 1, true
	}
	if lv.preN < rv.preN {
		return -1, true
	}
	if lv.preN > rv.preN {
		return 1, true
	}
	return 0, true
}

func parseDaemonVersion(value string) (daemonVersion, bool) {
	value = strings.TrimPrefix(strings.TrimSpace(value), "v")
	if value == "" {
		return daemonVersion{}, false
	}
	core, pre, hasPre := strings.Cut(value, "-")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return daemonVersion{}, false
	}
	nums := [3]int{}
	for i, part := range parts {
		parsed, err := strconv.Atoi(part)
		if err != nil || parsed < 0 {
			return daemonVersion{}, false
		}
		nums[i] = parsed
	}
	out := daemonVersion{major: nums[0], minor: nums[1], patch: nums[2], hasPre: hasPre}
	if hasPre {
		out.pre, out.preN = splitPrerelease(pre)
	}
	return out, true
}

func splitPrerelease(value string) (string, int) {
	value = strings.ToLower(strings.TrimSpace(value))
	i := len(value)
	for i > 0 && value[i-1] >= '0' && value[i-1] <= '9' {
		i--
	}
	n := 0
	if i < len(value) {
		n, _ = strconv.Atoi(value[i:])
	}
	label := strings.TrimRight(strings.TrimRight(value[:i], "."), "-")
	return label, n
}
