package editor

import (
	"bytes"
	"log/slog"
	"math"
	"strings"
	"testing"
)

func TestToInt(t *testing.T) {
	tests := []struct {
		name    string
		input   any
		want    int
		wantErr bool
	}{
		// Supported numeric types.
		{name: "int", input: int(42), want: 42},
		{name: "int32", input: int32(42), want: 42},
		{name: "int64", input: int64(42), want: 42},
		{name: "uint", input: uint(42), want: 42},
		{name: "uint32", input: uint32(42), want: 42},
		{name: "uint64", input: uint64(42), want: 42},
		{name: "float64", input: float64(42), want: 42},

		// float64 with fractional part is rejected.
		{name: "float64 fractional", input: float64(3.9), wantErr: true},

		// Negative values for signed types.
		{name: "int negative", input: int(-5), want: -5},
		{name: "int32 negative", input: int32(-42), want: -42},
		{name: "int64 negative", input: int64(-100), want: -100},

		// Zero values for each type.
		{name: "int zero", input: int(0), want: 0},
		{name: "int32 zero", input: int32(0), want: 0},
		{name: "int64 zero", input: int64(0), want: 0},
		{name: "uint zero", input: uint(0), want: 0},
		{name: "uint32 zero", input: uint32(0), want: 0},
		{name: "uint64 zero", input: uint64(0), want: 0},
		{name: "float64 zero", input: float64(0), want: 0},

		// Overflow/underflow.
		{name: "uint64 overflow", input: uint64(math.MaxUint64), wantErr: true},
		{name: "float64 overflow", input: float64(math.MaxFloat64), wantErr: true},
		{name: "float64 underflow", input: float64(-math.MaxFloat64), wantErr: true},

		// Unsupported types.
		{name: "string", input: "42", wantErr: true},
		{name: "bool", input: true, wantErr: true},
		{name: "nil", input: nil, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := toInt(tt.input)
			if tt.wantErr {
				if err == nil {
					t.Errorf("toInt(%v) = %d, want error", tt.input, got)
				}
				return
			}
			if err != nil {
				t.Errorf("toInt(%v) returned unexpected error: %v", tt.input, err)
				return
			}
			if got != tt.want {
				t.Errorf("toInt(%v) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

func TestParsePort(t *testing.T) {
	tests := []struct {
		name string
		v    any
		want int
		ok   bool
	}{
		{name: "positive int", v: int(8080), want: 8080, ok: true},
		{name: "positive float64", v: float64(3000), want: 3000, ok: true},
		{name: "positive string", v: "9090", want: 9090, ok: true},
		{name: "zero int", v: int(0), want: 0, ok: false},
		{name: "negative int", v: int(-1), want: 0, ok: false},
		{name: "negative string", v: "-1", want: 0, ok: false},
		{name: "zero string", v: "0", want: 0, ok: false},
		{name: "empty string", v: "", want: 0, ok: false},
		{name: "non-numeric string", v: "abc", want: 0, ok: false},
		{name: "nil", v: nil, want: 0, ok: false},
		{name: "max valid port int", v: int(65535), want: 65535, ok: true},
		{name: "max valid port string", v: "65535", want: 65535, ok: true},
		{name: "over limit int", v: int(65536), want: 0, ok: false},
		{name: "over limit string", v: "65536", want: 0, ok: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parsePort(tt.v)
			if ok != tt.ok || got != tt.want {
				t.Errorf("parsePort(%v) = (%d, %t), want (%d, %t)", tt.v, got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestMapToPreviewOptions(t *testing.T) {
	t.Run("all fields populated", func(t *testing.T) {
		m := map[string]any{
			"mkit":                map[string]any{"breaks": true},
			"katex":               map[string]any{"throwOnError": false},
			"uml":                 map[string]any{"server": "http://localhost"},
			"maid":                map[string]any{"theme": "dark"},
			"disable_sync_scroll": int(1),
			"sync_scroll_type":    "middle",
			"hide_yaml_meta":      float64(1),
			"sequence_diagrams":   map[string]any{"theme": "simple"},
			"flowchart_diagrams":  map[string]any{"x": 0},
			"content_editable":    int64(0),
			"disable_filename":    uint32(1),
			"toc":                 map[string]any{"listType": "ul"},
		}

		opts := mapToPreviewOptions(m, discardLogger())

		if opts.MarkdownIt == nil || opts.MarkdownIt["breaks"] != true {
			t.Errorf("MarkdownIt: got %v, want map with breaks=true", opts.MarkdownIt)
		}
		if opts.KaTeX == nil || opts.KaTeX["throwOnError"] != false {
			t.Errorf("KaTeX: got %v, want map with throwOnError=false", opts.KaTeX)
		}
		if opts.UML == nil || opts.UML["server"] != "http://localhost" {
			t.Errorf("UML: got %v, want map with server=http://localhost", opts.UML)
		}
		if opts.Mermaid == nil || opts.Mermaid["theme"] != "dark" {
			t.Errorf("Mermaid: got %v, want map with theme=dark", opts.Mermaid)
		}
		if opts.DisableSyncScroll != 1 {
			t.Errorf("DisableSyncScroll: got %d, want 1", opts.DisableSyncScroll)
		}
		if opts.SyncScrollType != "middle" {
			t.Errorf("SyncScrollType: got %q, want %q", opts.SyncScrollType, "middle")
		}
		if opts.HideYAMLMeta != 1 {
			t.Errorf("HideYAMLMeta: got %d, want 1", opts.HideYAMLMeta)
		}
		if opts.SequenceDiagrams == nil || opts.SequenceDiagrams["theme"] != "simple" {
			t.Errorf("SequenceDiagrams: got %v, want map with theme=simple", opts.SequenceDiagrams)
		}
		if opts.FlowchartDiagrams == nil {
			t.Errorf("FlowchartDiagrams: got nil, want non-nil map")
		}
		if opts.ContentEditable != 0 {
			t.Errorf("ContentEditable: got %d, want 0", opts.ContentEditable)
		}
		if opts.DisableFilename != 1 {
			t.Errorf("DisableFilename: got %d, want 1", opts.DisableFilename)
		}
		if opts.TOC == nil || opts.TOC["listType"] != "ul" {
			t.Errorf("TOC: got %v, want map with listType=ul", opts.TOC)
		}
	})

	t.Run("empty map returns zero-value options", func(t *testing.T) {
		opts := mapToPreviewOptions(map[string]any{}, discardLogger())
		// PreviewOptions contains map fields so it is not comparable with ==.
		// Check that all scalar fields are zero and all map fields are nil.
		if opts.DisableSyncScroll != 0 || opts.SyncScrollType != "" ||
			opts.HideYAMLMeta != 0 || opts.ContentEditable != 0 ||
			opts.DisableFilename != 0 {
			t.Errorf("empty map: scalar fields not zero: %+v", opts)
		}
		if opts.MarkdownIt != nil || opts.KaTeX != nil || opts.UML != nil ||
			opts.Mermaid != nil || opts.SequenceDiagrams != nil ||
			opts.FlowchartDiagrams != nil || opts.TOC != nil {
			t.Errorf("empty map: map fields not nil: %+v", opts)
		}
	})

	t.Run("nil map does not panic", func(t *testing.T) {
		// mapToPreviewOptions receives a nil map when there are no
		// preview options. Safe type assertions on a nil map always
		// return the zero value and ok=false, so this should not panic.
		opts := mapToPreviewOptions(nil, discardLogger())
		if opts.DisableSyncScroll != 0 || opts.SyncScrollType != "" ||
			opts.HideYAMLMeta != 0 || opts.ContentEditable != 0 ||
			opts.DisableFilename != 0 {
			t.Errorf("nil map: scalar fields not zero: %+v", opts)
		}
		if opts.MarkdownIt != nil || opts.KaTeX != nil || opts.UML != nil ||
			opts.Mermaid != nil || opts.SequenceDiagrams != nil ||
			opts.FlowchartDiagrams != nil || opts.TOC != nil {
			t.Errorf("nil map: map fields not nil: %+v", opts)
		}
	})

	t.Run("partial fields", func(t *testing.T) {
		m := map[string]any{
			"sync_scroll_type": "top",
			"hide_yaml_meta":   float64(1),
		}

		opts := mapToPreviewOptions(m, discardLogger())

		if opts.SyncScrollType != "top" {
			t.Errorf("SyncScrollType: got %q, want %q", opts.SyncScrollType, "top")
		}
		if opts.HideYAMLMeta != 1 {
			t.Errorf("HideYAMLMeta: got %d, want 1", opts.HideYAMLMeta)
		}
		// Unset fields should remain at zero value.
		if opts.MarkdownIt != nil {
			t.Errorf("MarkdownIt: got %v, want nil", opts.MarkdownIt)
		}
		if opts.DisableSyncScroll != 0 {
			t.Errorf("DisableSyncScroll: got %d, want 0", opts.DisableSyncScroll)
		}
	})

	t.Run("wrong type for numeric field uses default zero", func(t *testing.T) {
		// When a numeric field receives a non-numeric value (e.g. string
		// where int is expected), toInt returns an error and the field
		// keeps its zero value.
		m := map[string]any{
			"disable_sync_scroll": "yes",
			"hide_yaml_meta":      "true",
			"content_editable":    []int{1},
		}

		opts := mapToPreviewOptions(m, discardLogger())

		if opts.DisableSyncScroll != 0 {
			t.Errorf("DisableSyncScroll: got %d, want 0 (wrong type fallback)", opts.DisableSyncScroll)
		}
		if opts.HideYAMLMeta != 0 {
			t.Errorf("HideYAMLMeta: got %d, want 0 (wrong type fallback)", opts.HideYAMLMeta)
		}
		if opts.ContentEditable != 0 {
			t.Errorf("ContentEditable: got %d, want 0 (wrong type fallback)", opts.ContentEditable)
		}
	})

	t.Run("wrong type for map field uses nil and logs warning", func(t *testing.T) {
		// When a map field receives a non-map value, the type assertion
		// fails, the field stays nil, and a warning is logged.
		m := map[string]any{
			"mkit":  42,
			"katex": "not a map",
			"toc":   true,
		}

		var buf bytes.Buffer
		logger := slog.New(slog.NewTextHandler(&buf, nil))
		opts := mapToPreviewOptions(m, logger)

		if opts.MarkdownIt != nil {
			t.Errorf("MarkdownIt: got %v, want nil (wrong type fallback)", opts.MarkdownIt)
		}
		if opts.KaTeX != nil {
			t.Errorf("KaTeX: got %v, want nil (wrong type fallback)", opts.KaTeX)
		}
		if opts.TOC != nil {
			t.Errorf("TOC: got %v, want nil (wrong type fallback)", opts.TOC)
		}

		// Verify warnings were logged for each misconfigured map field.
		logged := buf.String()
		for _, key := range []string{"mkit", "katex", "toc"} {
			if !strings.Contains(logged, key) {
				t.Errorf("expected warning log for key %q, got: %s", key, logged)
			}
		}
	})
}

func TestExtractBufnrFromArgs(t *testing.T) {
	tests := []struct {
		name    string
		args    []any
		want    int
		wantErr string
	}{
		{
			name: "valid input with float64 bufnr",
			args: []any{map[string]any{"bufnr": float64(1)}},
			want: 1,
		},
		{
			name: "valid input with int bufnr",
			args: []any{map[string]any{"bufnr": int(7)}},
			want: 7,
		},
		{
			name:    "no args",
			args:    []any{},
			wantErr: "no args",
		},
		{
			name:    "non-map first arg",
			args:    []any{"not a map"},
			wantErr: "expected map",
		},
		{
			name:    "missing bufnr key",
			args:    []any{map[string]any{"other": 1}},
			wantErr: "missing bufnr key",
		},
		{
			name:    "non-numeric bufnr",
			args:    []any{map[string]any{"bufnr": "one"}},
			wantErr: "cannot convert",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := extractBufnrFromArgs(tt.args)
			if tt.wantErr != "" {
				if err == nil {
					t.Errorf("extractBufnrFromArgs(%v) = %d, want error containing %q", tt.args, got, tt.wantErr)
					return
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("extractBufnrFromArgs(%v) error = %q, want it to contain %q", tt.args, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Errorf("extractBufnrFromArgs(%v) returned unexpected error: %v", tt.args, err)
				return
			}
			if got != tt.want {
				t.Errorf("extractBufnrFromArgs(%v) = %d, want %d", tt.args, got, tt.want)
			}
		})
	}
}

func TestExtractBufnrFromMap(t *testing.T) {
	tests := []struct {
		name    string
		m       map[string]any
		want    int
		wantErr string
	}{
		{
			name: "valid input",
			m:    map[string]any{"bufnr": float64(5)},
			want: 5,
		},
		{
			name: "valid input with int",
			m:    map[string]any{"bufnr": int(3)},
			want: 3,
		},
		{
			name:    "nil map",
			m:       nil,
			wantErr: "nil data map",
		},
		{
			name:    "missing key",
			m:       map[string]any{"other": float64(5)},
			wantErr: "missing bufnr key",
		},
		{
			name:    "non-numeric value",
			m:       map[string]any{"bufnr": "five"},
			wantErr: "cannot convert",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := extractBufnrFromMap(tt.m)
			if tt.wantErr != "" {
				if err == nil {
					t.Errorf("extractBufnrFromMap(%v) = %d, want error containing %q", tt.m, got, tt.wantErr)
					return
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Errorf("extractBufnrFromMap(%v) error = %q, want it to contain %q", tt.m, err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Errorf("extractBufnrFromMap(%v) returned unexpected error: %v", tt.m, err)
				return
			}
			if got != tt.want {
				t.Errorf("extractBufnrFromMap(%v) = %d, want %d", tt.m, got, tt.want)
			}
		})
	}
}
