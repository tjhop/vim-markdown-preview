package editor

import (
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strconv"

	"github.com/tjhop/vim-markdown-preview/internal/config"
)

// toInt converts various numeric types that may come back from msgpack or JSON
// decoding into a plain int. Both Neovim (msgpack) and Vim 8 (JSON) can
// produce different Go types for the same numeric value depending on how the
// decoder handles it. Returns an error if the value overflows int.
func toInt(v any) (int, error) {
	switch n := v.(type) {
	case int:
		return n, nil
	case int32:
		return int(n), nil
	case int64:
		if n < math.MinInt || n > math.MaxInt {
			return 0, fmt.Errorf("int64 %d overflows int", n)
		}
		return int(n), nil
	case uint:
		if n > math.MaxInt {
			return 0, fmt.Errorf("uint %d overflows int", n)
		}
		return int(n), nil
	case uint32:
		// On 64-bit targets int is 64 bits and uint32 always fits.
		// On 32-bit targets int is 32 bits and uint32 max (4,294,967,295)
		// overflows. Convert through uint64 so the comparison with
		// MaxInt compiles on both word sizes.
		if uint64(n) > math.MaxInt {
			return 0, fmt.Errorf("uint32 %d overflows int", n)
		}
		return int(n), nil
	case uint64:
		if n > math.MaxInt {
			return 0, fmt.Errorf("uint64 %d overflows int", n)
		}
		return int(n), nil
	case float64:
		// JSON decoding produces float64 for all numbers. Reject
		// non-integer values (e.g. 1.5) to catch malformed data
		// rather than silently truncating. Precision loss for
		// integers > 2^53 is acceptable because the values being
		// converted are buffer numbers, line counts, and small
		// config flags -- all well within float64's exact range.
		if n != math.Trunc(n) {
			return 0, fmt.Errorf("float64 %g is not a whole number", n)
		}
		// float64(math.MaxInt) rounds up to 2^63 (one past the true max),
		// so `n > math.MaxInt` would allow exactly 9.223372036854776e18 to
		// pass and int(n) would wrap to math.MinInt. Use `n >= 1<<63`
		// instead. float64(math.MinInt) is exactly -2^63, so the lower
		// bound comparison is correct and does not need adjustment.
		if n < math.MinInt || n >= 1<<63 {
			return 0, fmt.Errorf("float64 %g overflows int", n)
		}
		return int(n), nil
	default:
		return 0, fmt.Errorf("cannot convert %T to int", v)
	}
}

// parsePort attempts to interpret v as a valid port number (1–65535). The
// value may be a numeric type (from msgpack or JSON decoding) or a string
// representation. Returns the port and true on success, or 0 and false if
// the value is not a valid port. Used by both Neovim and Vim FetchConfig
// paths to handle the g:mkdp_port variable, which may be set as an int or
// a string.
func parsePort(v any) (int, bool) {
	isValidPort := func(p int) bool { return p > 0 && p <= 65535 }
	if p, err := toInt(v); err == nil && isValidPort(p) {
		return p, true
	}
	if s, ok := v.(string); ok && s != "" {
		if p, err := strconv.Atoi(s); err == nil && isValidPort(p) {
			return p, true
		}
	}
	return 0, false
}

// errKeyAbsent is returned by intFromMap when the requested key is not present
// in the map. Callers use errors.Is(err, errKeyAbsent) to distinguish "key
// missing" (not a misconfiguration; preserve the caller's default) from a type
// conversion error (misconfiguration; log a warning).
var errKeyAbsent = errors.New("key not present in map")

// intFromMap extracts an integer value from m at the given key using toInt.
// Returns (value, nil) if the key exists and converts successfully.
// Returns (0, errKeyAbsent) if the key is absent (not a misconfiguration).
// Returns (0, err) if the key exists but cannot be converted due to a type
// mismatch or overflow, allowing callers to distinguish and log the error.
func intFromMap(m map[string]any, key string) (int, error) {
	v, ok := m[key]
	if !ok {
		return 0, errKeyAbsent
	}
	n, err := toInt(v)
	if err != nil {
		return 0, err
	}
	return n, nil
}

// mapToPreviewOptions converts a raw map (from either Neovim or Vim) into a
// typed PreviewOptions struct, using safe type assertions with fallback
// defaults. Used by both NeovimClient.FetchConfig and VimClient.FetchConfig.
// Type mismatches are logged as warnings; the field retains its zero value
// so the rest of the config is unaffected.
func mapToPreviewOptions(m map[string]any, logger *slog.Logger) config.PreviewOptions {
	var opts config.PreviewOptions

	applyIntField := func(key string, dest *int) {
		n, err := intFromMap(m, key)
		switch {
		case err == nil:
			*dest = n
		case errors.Is(err, errKeyAbsent):
			// Key absent is not an error; preserve the caller's default.
		default:
			logger.Warn("config decode failed", "key", key, "err", err)
		}
	}

	applyMapField := func(key string, dest *map[string]any) {
		v, exists := m[key]
		if !exists {
			return
		}
		if mv, ok := v.(map[string]any); ok {
			*dest = mv
		} else {
			logger.Warn("config decode failed", "key", key, "err", fmt.Errorf("expected map, got %T", v))
		}
	}

	// config.Opt* constants are defined only for keys that also appear in
	// config.NewStandaloneRefreshData (shared between editor and standalone
	// paths). Keys used only here use bare string literals because defining
	// a constant for a single call site would add indirection without benefit.
	applyMapField("mkit", &opts.MarkdownIt)
	applyMapField("katex", &opts.KaTeX)
	applyMapField("uml", &opts.UML)
	applyMapField("maid", &opts.Mermaid)
	applyIntField("disable_sync_scroll", &opts.DisableSyncScroll)
	if v, ok := m[config.OptKeySyncScrollType]; ok {
		if s, ok := v.(string); ok {
			opts.SyncScrollType = s
		} else {
			logger.Warn("config decode failed", "key", config.OptKeySyncScrollType, "err", fmt.Errorf("expected string, got %T", v))
		}
	}
	applyIntField(config.OptKeyHideYAMLMeta, &opts.HideYAMLMeta)
	applyMapField("sequence_diagrams", &opts.SequenceDiagrams)
	applyMapField("flowchart_diagrams", &opts.FlowchartDiagrams)
	applyIntField("content_editable", &opts.ContentEditable)
	applyIntField("disable_filename", &opts.DisableFilename)
	applyMapField("toc", &opts.TOC)

	return opts
}

// extractBufnrFromMap gets the bufnr value from a JSON-decoded map.
// Returns an error if the map is nil, the key is missing, or the value
// is not a supported numeric type. Used by both the Vim dispatchNotification
// (which receives the map directly) and extractBufnrFromArgs below (which
// unwraps Neovim's variadic args first).
func extractBufnrFromMap(m map[string]any) (int, error) {
	if m == nil {
		return 0, errors.New("nil data map")
	}
	v, ok := m["bufnr"]
	if !ok {
		return 0, errors.New("missing bufnr key")
	}
	return toInt(v)
}

// extractBufnrFromArgs safely extracts the bufnr field from notification args.
// Neovim sends notifications as rpcnotify(chan, event, {data}), so args
// is typically [{bufnr: N}]. Delegates to extractBufnrFromMap after
// unwrapping the args slice.
func extractBufnrFromArgs(args []any) (int, error) {
	if len(args) == 0 {
		return 0, errors.New("no args")
	}

	m, ok := args[0].(map[string]any)
	if !ok {
		return 0, fmt.Errorf("expected map, got %T", args[0])
	}

	return extractBufnrFromMap(m)
}

// bytesToStrings converts a slice of byte slices (nvim buffer lines)
// to a slice of strings.
func bytesToStrings(raw [][]byte) []string {
	lines := make([]string, len(raw))
	for i, line := range raw {
		lines[i] = string(line)
	}
	return lines
}
