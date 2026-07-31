package agent

import (
	"strings"
	"testing"
)

func TestInstallRequiresControlURLAndAPIKey(t *testing.T) {
	t.Setenv("GESTA_CONTROL_URL", "")
	t.Setenv("GESTA_API_KEY", "")
	t.Setenv("GESTA_APIKEY", "")

	err := install(nil)
	if err == nil || !strings.Contains(err.Error(), "--control-url is required") {
		t.Fatalf("install error = %v, want missing control-url", err)
	}

	err = install([]string{"--control-url", "http://127.0.0.1:8080"})
	if err == nil || !strings.Contains(err.Error(), "--apikey is required") {
		t.Fatalf("install error = %v, want missing apikey", err)
	}
}
