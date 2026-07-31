package agent

import (
	"fmt"
	"os"
)

// Install-progress styling. These mirror scripts/install.sh so the agent's own
// "install" subcommand output blends with the shell installer's lines instead of
// appearing as plain interleaved text. Following the same convention as the
// shell, normal progress (uiOK) goes to stdout and diagnostics (uiWarn) go to
// stderr, and color for each stream is decided independently: color is emitted
// only when that stream is a terminal and the user has not opted out via
// NO_COLOR / TERM=dumb, so redirecting either stream stays clean.
var (
	outReset = ""
	outDim   = ""
	outGreen = ""

	errReset  = ""
	errYellow = ""
)

func init() {
	if streamColorEnabled(os.Stdout) {
		outReset = "\033[0m"
		outDim = "\033[2m"
		outGreen = "\033[32m"
	}
	if streamColorEnabled(os.Stderr) {
		errReset = "\033[0m"
		errYellow = "\033[33m"
	}
}

func streamColorEnabled(f *os.File) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if os.Getenv("TERM") == "dumb" {
		return false
	}
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// uiOK prints a green ✓ success line to stdout, optionally followed by a dimmed
// detail (e.g. the path a hook was written to) on the same line.
func uiOK(msg, detail string) {
	if detail != "" {
		fmt.Fprintf(os.Stdout, "%s✓%s %s %s(%s)%s\n", outGreen, outReset, msg, outDim, detail, outReset)
		return
	}
	fmt.Fprintf(os.Stdout, "%s✓%s %s\n", outGreen, outReset, msg)
}

// uiWarn prints a yellow ! advisory line to stderr.
func uiWarn(msg string) {
	fmt.Fprintf(os.Stderr, "%s!%s %s\n", errYellow, errReset, msg)
}
