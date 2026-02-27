package browser

import (
	"log/slog"
	"slices"
	"strings"
	"testing"
)

// TestOpenErrorPath verifies that Open returns an error when the browser
// command does not exist. This exercises the exec.Command(...).Start()
// failure path that openCommand-only tests do not reach. The error must
// indicate that the binary was not found, distinguishing a missing-binary
// failure from unrelated errors such as permission denied.
func TestOpenErrorPath(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)

	err := Open("http://localhost:1234", "/nonexistent/browser-command-abc123", logger)
	if err == nil {
		t.Fatal("expected Open to return an error for nonexistent browser command, got nil")
	}

	// On Linux, exec.Cmd.Start returns an *os.PathError wrapping syscall.ENOENT
	// when the binary does not exist. The error string always contains
	// "no such file or directory" in that case.
	msg := err.Error()
	if !strings.Contains(msg, "no such file or directory") {
		t.Errorf("expected error to indicate binary not found (\"no such file or directory\"), got: %v", msg)
	}
}

// TestOpenCommand verifies the command construction for opening URLs.
// On standard Linux it exercises the xdg-open path; under WSL it exercises
// cmd.exe. The darwin and windows branches of openCommand are not covered
// here because the test file has no build constraint and runtime.GOOS
// determines which branch executes. Platform-specific coverage would
// require either build-tagged test files or mocking runtime.GOOS.
//
// Platform coverage summary:
//   - Linux (standard): exercises xdg-open (default) and custom browser paths.
//   - Linux (WSL):      exercises cmd.exe /c start (default) and custom browser paths.
//   - darwin, windows:  not tested; require build-tagged test files.
//
// The isWSL() result is cached in a package-level sync.Once, so both
// branches cannot be tested in a single process. See QUALITY.md.
func TestOpenCommand(t *testing.T) {
	wsl := isWSL()

	// Determine the expected command for default browser (empty browserCmd)
	// based on whether we're running under WSL or standard Linux.
	var defaultName string
	var defaultArgs []string
	if wsl {
		defaultName = "cmd.exe"
		defaultArgs = []string{"/c", "start", "", "http://localhost:8080"}
	} else {
		defaultName = "xdg-open"
		defaultArgs = []string{"http://localhost:8080"}
	}

	tests := []struct {
		name       string
		url        string
		browserCmd string
		wantName   string
		wantArgs   []string
	}{
		{
			name:       "default browser",
			url:        "http://localhost:8080",
			browserCmd: "",
			wantName:   defaultName,
			wantArgs:   defaultArgs,
		},
		{
			name:       "custom browser",
			url:        "http://localhost:9090",
			browserCmd: "firefox",
			wantName:   "firefox",
			wantArgs:   []string{"http://localhost:9090"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotName, gotArgs := openCommand(tt.url, tt.browserCmd)

			if gotName != tt.wantName {
				t.Errorf("openCommand(%q, %q) name = %q, want %q",
					tt.url, tt.browserCmd, gotName, tt.wantName)
			}
			if !slices.Equal(gotArgs, tt.wantArgs) {
				t.Errorf("openCommand(%q, %q) args = %v, want %v",
					tt.url, tt.browserCmd, gotArgs, tt.wantArgs)
			}
		})
	}
}
