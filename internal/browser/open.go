// Package browser provides cross-platform browser opening with WSL detection.
package browser

import (
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
)

// Open launches the given URL in the user's default browser.
// If browser is non-empty, it is used as the browser command.
// A background goroutine waits for the process to exit and logs any error
// (e.g. bad path, permission denied) so early failures are not silently lost.
func Open(url string, browserCmd string, logger *slog.Logger) error {
	name, args := openCommand(url, browserCmd)
	cmd := exec.Command(name, args...)

	if err := cmd.Start(); err != nil {
		return err
	}

	go func() {
		if err := cmd.Wait(); err != nil {
			logger.Error("browser process exited with error", "err", err, "cmd", name, "url", url)
		}
	}()

	return nil
}

// openCommand returns the command name and arguments to open a URL in
// the user's browser, accounting for OS and optional browser override.
func openCommand(url string, browserCmd string) (string, []string) {
	// strings.Fields splits on whitespace so multi-word commands like
	// "firefox --private-window" work correctly; the first token is the
	// executable and the rest are prepended before the URL.
	// Hoisted here so both the non-darwin early return and the darwin
	// case share a single split.
	parts := strings.Fields(browserCmd)

	// Non-darwin platforms invoke the custom browser command directly.
	// Darwin needs special handling via 'open -a', so it falls through to the switch.
	if len(parts) > 0 && runtime.GOOS != "darwin" {
		// Cap the sub-slice so append always allocates a new
		// backing array, regardless of Fields' allocation strategy.
		return parts[0], append(parts[1:len(parts):len(parts)], url)
	}

	// At this point browserCmd is empty for all non-darwin platforms.
	// The early return above handles non-empty browserCmd on non-darwin, so
	// each case below only needs to handle the default browser for its OS.
	switch runtime.GOOS {
	case "linux":
		if isWSL() {
			return "cmd.exe", []string{"/c", "start", "", url}
		}
		return "xdg-open", []string{url}

	case "darwin":
		if len(parts) > 0 {
			// Find the first flag-like token (starts with '-').
			// Everything before it is the application name (which may
			// contain spaces, e.g. "Google Chrome"); everything from
			// it onward is passed to the application via --args.
			flagIdx := len(parts)
			for i, p := range parts {
				if strings.HasPrefix(p, "-") {
					flagIdx = i
					break
				}
			}

			appName := strings.Join(parts[:flagIdx], " ")
			args := []string{"-a", appName, url}
			if flagIdx < len(parts) {
				args = append(args, "--args")
				args = append(args, parts[flagIdx:]...)
			}
			return "open", args
		}
		return "open", []string{url}

	case "windows":
		return "cmd.exe", []string{"/c", "start", "", url}

	default:
		// Best effort: assume xdg-open is available.
		return "xdg-open", []string{url}
	}
}

var (
	wslDetectionOnce sync.Once
	wslDetected      bool
)

// isWSL returns true if running inside Windows Subsystem for Linux.
// The result is cached after the first call: /proc/version does not change
// at runtime, so repeated I/O is wasteful.
func isWSL() bool {
	wslDetectionOnce.Do(func() {
		data, err := os.ReadFile("/proc/version")
		if err != nil {
			return
		}
		wslDetected = strings.Contains(strings.ToLower(string(data)), "microsoft")
	})
	return wslDetected
}
