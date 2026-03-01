package ocr

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

func writeTinyPNG(t *testing.T, path string) {
	t.Helper()
	// 1x1 transparent PNG.
	data := []byte{
		0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
		0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
		0x89, 0x00, 0x00, 0x00, 0x0a, 0x49, 0x44, 0x41,
		0x54, 0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00,
		0x05, 0x00, 0x01, 0x0d, 0x0a, 0x2d, 0xb4, 0x00,
		0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44, 0xae,
		0x42, 0x60, 0x82,
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("write png: %v", err)
	}
}

func writeConfig(t *testing.T, root string, extra []string) string {
	t.Helper()
	lines := []string{
		"ocr:",
		"  api_base: https://example.com/v1",
		"  api_key: test-key",
		"  model: test-model",
	}
	lines = append(lines, extra...)
	cfg := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(cfg, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return cfg
}

func TestBuildPromptDefaultContainsCriticalInstructions(t *testing.T) {
	prompt, err := buildPrompt(OCRConfig{APIBase: "https://x/v1", APIKey: "k", Model: "m"}, 3)
	if err != nil {
		t.Fatalf("build prompt: %v", err)
	}
	if !strings.Contains(prompt, "Do not include row numbers in output text.") {
		t.Fatalf("missing row number rule in prompt: %q", prompt)
	}
	if !strings.Contains(prompt, "exactly 3 rows") {
		t.Fatalf("missing row count in prompt: %q", prompt)
	}
	if !strings.Contains(prompt, "NOT raw \\N") {
		t.Fatalf("missing escaped line-break rule in prompt: %q", prompt)
	}
}

func TestParseLinesPreservesEscapedNewlinesInsideItems(t *testing.T) {
	parsed, err := parseLinesFromResponse(`{"lines":["hello\nworld","a\rb"]}`, 2)
	if err != nil {
		t.Fatalf("parse lines: %v", err)
	}
	if got, want := parsed, []string{"hello\nworld", "a\rb"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("unexpected parsed lines: got=%v want=%v", got, want)
	}
}

func TestParseLinesOverflowMergesTailIntoFinalItem(t *testing.T) {
	parsed, err := parseLinesFromResponse(`{"lines":["a","b","c"]}`, 2)
	if err != nil {
		t.Fatalf("parse lines: %v", err)
	}
	if got, want := parsed, []string{"a", "b c"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("unexpected parsed lines: got=%v want=%v", got, want)
	}
}

func TestParseLinesFallbackSalvagesTruncatedJSON(t *testing.T) {
	parsed, err := parseLinesFromResponse(`{"lines":["first","second"`, 2)
	if err != nil {
		t.Fatalf("parse lines fallback: %v", err)
	}
	if got, want := parsed, []string{"first", "second"}; fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("unexpected parsed lines from fallback: got=%v want=%v", got, want)
	}
}

func TestBuildPromptInvalidTemplateReturnsError(t *testing.T) {
	_, err := buildPrompt(OCRConfig{
		APIBase:        "https://x/v1",
		APIKey:         "k",
		Model:          "m",
		PromptTemplate: "rows={expected_count} bad={oops}",
	}, 2)
	if err == nil {
		t.Fatal("expected invalid template error, got nil")
	}
	if !strings.Contains(err.Error(), "invalid ocr.prompt_template") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadOCRConfigValidationErrors(t *testing.T) {
	root := t.TempDir()

	t.Run("missing required fields", func(t *testing.T) {
		cfgPath := filepath.Join(root, "missing-required.yaml")
		yaml := strings.Join([]string{
			"ocr:",
			"  api_base: https://example.com/v1",
			"  api_key: test-key",
		}, "\n")
		if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
			t.Fatalf("write config: %v", err)
		}

		_, err := LoadOCRConfig(cfgPath)
		if err == nil {
			t.Fatal("expected missing required fields error, got nil")
		}
		if !strings.Contains(err.Error(), "missing required fields") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("negative max retries", func(t *testing.T) {
		cfgPath := writeConfig(t, root, []string{"  max_retries: -1"})
		_, err := LoadOCRConfig(cfgPath)
		if err == nil {
			t.Fatal("expected max_retries validation error, got nil")
		}
		if !strings.Contains(err.Error(), "ocr.max_retries must be >= 0") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("negative retry backoff", func(t *testing.T) {
		cfgPath := writeConfig(t, root, []string{"  retry_backoff_seconds: -0.5"})
		_, err := LoadOCRConfig(cfgPath)
		if err == nil {
			t.Fatal("expected retry_backoff_seconds validation error, got nil")
		}
		if !strings.Contains(err.Error(), "ocr.retry_backoff_seconds must be >= 0") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("non-positive timeout", func(t *testing.T) {
		cfgPath := writeConfig(t, root, []string{"  timeout_seconds: 0"})
		_, err := LoadOCRConfig(cfgPath)
		if err == nil {
			t.Fatal("expected timeout validation error, got nil")
		}
		if !strings.Contains(err.Error(), "ocr.timeout_seconds must be > 0") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestLoadOCRConfigFileReadAndParseErrors(t *testing.T) {
	root := t.TempDir()

	t.Run("missing file", func(t *testing.T) {
		_, err := LoadOCRConfig(filepath.Join(root, "does-not-exist.yaml"))
		if err == nil {
			t.Fatal("expected missing file error, got nil")
		}
		if !strings.Contains(err.Error(), "config file not found") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("invalid yaml", func(t *testing.T) {
		cfgPath := filepath.Join(root, "bad.yaml")
		if err := os.WriteFile(cfgPath, []byte("ocr:\n  api_base: ["), 0o644); err != nil {
			t.Fatalf("write bad yaml: %v", err)
		}
		_, err := LoadOCRConfig(cfgPath)
		if err == nil {
			t.Fatal("expected parse YAML error, got nil")
		}
		if !strings.Contains(err.Error(), "parse YAML config") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("read file failure", func(t *testing.T) {
		// Opening a directory with os.ReadFile fails consistently across platforms.
		_, err := LoadOCRConfig(root)
		if err == nil {
			t.Fatal("expected read config file error, got nil")
		}
		if !strings.Contains(err.Error(), "read config file") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestRunOCROnOutputBackfillsTimeline(t *testing.T) {
	root := t.TempDir()
	cfg := writeConfig(t, root, nil)
	writeTinyPNG(t, filepath.Join(root, "sheet_0001.png"))
	writeTinyPNG(t, filepath.Join(root, "sheet_0002.png"))

	mapping := map[string]any{
		"items": []map[string]any{
			{"cue_index": 1, "start_ms": 0, "end_ms": 1000, "sheet": "sheet_0001.png", "position_in_sheet": 1},
			{"cue_index": 2, "start_ms": 1000, "end_ms": 2000, "sheet": "sheet_0001.png", "position_in_sheet": 2},
			{"cue_index": 3, "start_ms": 2000, "end_ms": 3000, "sheet": "sheet_0002.png", "position_in_sheet": 1},
		},
	}
	b, _ := json.Marshal(mapping)
	if err := os.WriteFile(filepath.Join(root, "mapping.json"), b, 0o644); err != nil {
		t.Fatalf("write mapping: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "timeline.srt"), nil, 0o644); err != nil {
		t.Fatalf("write timeline: %v", err)
	}

	orig := callSheetLinesWithRetry
	callSheetLinesWithRetry = func(_ OCRConfig, imagePath string, expectedCount int) ([]string, error) {
		switch filepath.Base(imagePath) {
		case "sheet_0001.png":
			if expectedCount != 2 {
				return nil, fmt.Errorf("expectedCount=%d", expectedCount)
			}
			return []string{"line a", "line b"}, nil
		case "sheet_0002.png":
			if expectedCount != 1 {
				return nil, fmt.Errorf("expectedCount=%d", expectedCount)
			}
			return []string{"line c"}, nil
		default:
			return nil, fmt.Errorf("unexpected sheet: %s", imagePath)
		}
	}
	t.Cleanup(func() { callSheetLinesWithRetry = orig })

	outPath, err := RunOCROnOutput(root, cfg, true, nil)
	if err != nil {
		t.Fatalf("run ocr: %v", err)
	}
	if filepath.Base(outPath) != "timeline.srt" {
		t.Fatalf("unexpected output file: %s", outPath)
	}

	content, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	text := string(content)
	if !strings.Contains(text, "line a") || !strings.Contains(text, "line b") || !strings.Contains(text, "line c") {
		t.Fatalf("timeline does not contain OCR lines: %q", text)
	}
}

func TestRunOCROnOutputReportsProgressAndWritesNonOverwriteFile(t *testing.T) {
	root := t.TempDir()
	cfg := writeConfig(t, root, nil)
	writeTinyPNG(t, filepath.Join(root, "sheet_0001.png"))
	writeTinyPNG(t, filepath.Join(root, "sheet_0002.png"))

	mapping := map[string]any{
		"items": []map[string]any{
			{"cue_index": 2, "start_ms": 1000, "end_ms": 2000, "sheet": "sheet_0002.png", "position_in_sheet": 1},
			{"cue_index": 1, "start_ms": 0, "end_ms": 1000, "sheet": "sheet_0001.png", "position_in_sheet": 1},
		},
	}
	b, _ := json.Marshal(mapping)
	if err := os.WriteFile(filepath.Join(root, "mapping.json"), b, 0o644); err != nil {
		t.Fatalf("write mapping: %v", err)
	}

	orig := callSheetLinesWithRetry
	callSheetLinesWithRetry = func(_ OCRConfig, imagePath string, expectedCount int) ([]string, error) {
		switch filepath.Base(imagePath) {
		case "sheet_0001.png":
			if expectedCount != 1 {
				return nil, fmt.Errorf("expectedCount=%d", expectedCount)
			}
			return []string{"first line"}, nil
		case "sheet_0002.png":
			if expectedCount != 1 {
				return nil, fmt.Errorf("expectedCount=%d", expectedCount)
			}
			return []string{"second line"}, nil
		default:
			return nil, fmt.Errorf("unexpected sheet: %s", imagePath)
		}
	}
	t.Cleanup(func() { callSheetLinesWithRetry = orig })

	type progressEvent struct {
		done      int
		total     int
		sheetName string
	}
	var events []progressEvent
	progress := func(done, total int, sheetName string) {
		events = append(events, progressEvent{done: done, total: total, sheetName: sheetName})
	}

	outPath, err := RunOCROnOutput(root, cfg, false, progress)
	if err != nil {
		t.Fatalf("run ocr: %v", err)
	}
	if filepath.Base(outPath) != "timeline.ocr.srt" {
		t.Fatalf("unexpected output file when overwrite disabled: %s", outPath)
	}

	if len(events) != 2 {
		t.Fatalf("unexpected progress callback count: got=%d want=2", len(events))
	}
	if events[0] != (progressEvent{done: 1, total: 2, sheetName: "sheet_0001.png"}) {
		t.Fatalf("unexpected first progress event: %+v", events[0])
	}
	if events[1] != (progressEvent{done: 2, total: 2, sheetName: "sheet_0002.png"}) {
		t.Fatalf("unexpected second progress event: %+v", events[1])
	}
}

func TestRunOCROnOutputMissingMappingAndEmptyMapping(t *testing.T) {
	root := t.TempDir()
	cfg := writeConfig(t, root, nil)

	t.Run("missing mapping.json", func(t *testing.T) {
		_, err := RunOCROnOutput(root, cfg, true, nil)
		if err == nil {
			t.Fatal("expected missing mapping.json error, got nil")
		}
		if !strings.Contains(err.Error(), "mapping.json not found") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("mapping has no items", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(root, "mapping.json"), []byte(`{"items":[]}`), 0o644); err != nil {
			t.Fatalf("write mapping: %v", err)
		}
		_, err := RunOCROnOutput(root, cfg, true, nil)
		if err == nil {
			t.Fatal("expected empty mapping error, got nil")
		}
		if !strings.Contains(err.Error(), "mapping.json contains no items") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestRunOCROnOutputMissingSheetImage(t *testing.T) {
	root := t.TempDir()
	cfg := writeConfig(t, root, nil)
	if err := os.WriteFile(filepath.Join(root, "mapping.json"), []byte(`{"items":[{"cue_index":1,"start_ms":0,"end_ms":1000,"sheet":"sheet_0001.png","position_in_sheet":1}]}`), 0o644); err != nil {
		t.Fatalf("write mapping: %v", err)
	}

	_, err := RunOCROnOutput(root, cfg, true, nil)
	if err == nil {
		t.Fatal("expected missing sheet image error, got nil")
	}
	if !strings.Contains(err.Error(), "sheet image missing") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunOCROnOutputFallsBackToImageMarkerWhenOCRTextEmpty(t *testing.T) {
	root := t.TempDir()
	cfg := writeConfig(t, root, nil)
	writeTinyPNG(t, filepath.Join(root, "sheet_0001.png"))

	mapping := map[string]any{
		"items": []map[string]any{
			{"cue_index": 1, "start_ms": 0, "end_ms": 1000, "sheet": "sheet_0001.png", "position_in_sheet": 7},
		},
	}
	b, _ := json.Marshal(mapping)
	if err := os.WriteFile(filepath.Join(root, "mapping.json"), b, 0o644); err != nil {
		t.Fatalf("write mapping: %v", err)
	}

	orig := callSheetLinesWithRetry
	callSheetLinesWithRetry = func(_ OCRConfig, _ string, _ int) ([]string, error) {
		return []string{"   "}, nil
	}
	t.Cleanup(func() { callSheetLinesWithRetry = orig })

	outPath, err := RunOCROnOutput(root, cfg, true, nil)
	if err != nil {
		t.Fatalf("run ocr: %v", err)
	}
	content, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if !strings.Contains(string(content), "[img:sheet_0001.png#07]") {
		t.Fatalf("timeline does not contain marker fallback: %q", string(content))
	}
}

func TestRunOCROnOutputPreservesMultilineSingleCue(t *testing.T) {
	root := t.TempDir()
	cfg := writeConfig(t, root, nil)
	writeTinyPNG(t, filepath.Join(root, "sheet_0001.png"))

	mapping := map[string]any{
		"items": []map[string]any{
			{"cue_index": 1, "start_ms": 0, "end_ms": 1000, "sheet": "sheet_0001.png", "position_in_sheet": 1},
		},
	}
	b, _ := json.Marshal(mapping)
	if err := os.WriteFile(filepath.Join(root, "mapping.json"), b, 0o644); err != nil {
		t.Fatalf("write mapping: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "timeline.srt"), nil, 0o644); err != nil {
		t.Fatalf("write timeline: %v", err)
	}

	orig := callSheetLinesWithRetry
	callSheetLinesWithRetry = func(_ OCRConfig, _ string, _ int) ([]string, error) {
		return []string{"line1\nline2"}, nil
	}
	t.Cleanup(func() { callSheetLinesWithRetry = orig })

	outPath, err := RunOCROnOutput(root, cfg, true, nil)
	if err != nil {
		t.Fatalf("run ocr: %v", err)
	}
	content, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if !strings.Contains(string(content), "line1\nline2") {
		t.Fatalf("timeline does not preserve multiline payload: %q", string(content))
	}
}

func TestSheetLinesWithRetryRetriesTransientFailures(t *testing.T) {
	cfg := OCRConfig{MaxRetries: 2, RetryBackoffSeconds: 0}
	origCall := callSheetLines
	origSleep := sleepFn
	defer func() {
		callSheetLines = origCall
		sleepFn = origSleep
	}()
	sleepFn = func(_ time.Duration) {}

	calls := 0
	callSheetLines = func(_ OCRConfig, _ string, _ int) ([]string, error) {
		calls++
		if calls == 1 {
			return nil, context.DeadlineExceeded
		}
		return []string{"ok"}, nil
	}

	if _, err := ocrSheetLinesWithRetry(cfg, "sheet_0001.png", 1); err != nil {
		t.Fatalf("retry wrapper returned error: %v", err)
	}
	if calls != 2 {
		t.Fatalf("unexpected attempts: got=%d want=2", calls)
	}
}

func TestShouldRetryOCRErrorClassification(t *testing.T) {
	t.Run("nil is non-retryable", func(t *testing.T) {
		if shouldRetryOCRError(nil) {
			t.Fatal("expected nil error to be non-retryable")
		}
	})

	t.Run("retries timeout context error", func(t *testing.T) {
		if !shouldRetryOCRError(context.DeadlineExceeded) {
			t.Fatal("expected timeout context error to be retryable")
		}
	})

	t.Run("does not retry canceled context error", func(t *testing.T) {
		if shouldRetryOCRError(context.Canceled) {
			t.Fatal("expected canceled context error to be non-retryable")
		}
	})

	t.Run("retries timeout network error", func(t *testing.T) {
		err := &url.Error{Err: &net.DNSError{IsTimeout: true}}
		if !shouldRetryOCRError(err) {
			t.Fatal("expected timeout network error to be retryable")
		}
	})

	t.Run("retries 429 and 5xx", func(t *testing.T) {
		if !shouldRetryOCRError(&ocrHTTPStatusError{StatusCode: http.StatusTooManyRequests}) {
			t.Fatal("expected 429 to be retryable")
		}
		if !shouldRetryOCRError(&ocrHTTPStatusError{StatusCode: http.StatusBadGateway}) {
			t.Fatal("expected 5xx to be retryable")
		}
	})

	t.Run("does not retry deterministic 4xx", func(t *testing.T) {
		if shouldRetryOCRError(&ocrHTTPStatusError{StatusCode: http.StatusBadRequest}) {
			t.Fatal("expected 400 to be non-retryable")
		}
		if shouldRetryOCRError(&ocrHTTPStatusError{StatusCode: http.StatusUnauthorized}) {
			t.Fatal("expected 401 to be non-retryable")
		}
	})

	t.Run("does not retry schema errors", func(t *testing.T) {
		if shouldRetryOCRError(&ocrSchemaError{Message: "decode OCR response JSON: invalid character"}) {
			t.Fatal("expected schema error to be non-retryable")
		}
	})

	t.Run("does not retry non-timeout network error", func(t *testing.T) {
		err := &url.Error{Err: &net.DNSError{IsTimeout: false}}
		if shouldRetryOCRError(err) {
			t.Fatal("expected non-timeout network error to be non-retryable")
		}
	})
}

func TestSheetLinesWithRetryStopsAfterNonRetryableFailure(t *testing.T) {
	cfg := OCRConfig{MaxRetries: 3, RetryBackoffSeconds: 0}
	origCall := callSheetLines
	origSleep := sleepFn
	defer func() {
		callSheetLines = origCall
		sleepFn = origSleep
	}()
	sleepFn = func(_ time.Duration) {}

	calls := 0
	callSheetLines = func(_ OCRConfig, _ string, _ int) ([]string, error) {
		calls++
		return nil, &ocrHTTPStatusError{
			StatusCode: http.StatusBadRequest,
			URL:        "https://example.test/v1/chat/completions",
			Detail:     "invalid request",
		}
	}

	_, err := ocrSheetLinesWithRetry(cfg, "sheet_0001.png", 1)
	if err == nil {
		t.Fatal("expected retry wrapper to return error")
	}
	if calls != 1 {
		t.Fatalf("unexpected attempts for non-retryable error: got=%d want=1", calls)
	}
}

func TestParseLinesFromResponseErrorsWhenLinesMissing(t *testing.T) {
	_, err := parseLinesFromResponse(`{"not_lines":["a"]}`, 1)
	if err == nil {
		t.Fatal("expected error when lines field is missing")
	}
	if !strings.Contains(err.Error(), `"lines" field is missing`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseLinesFromResponseErrorsWhenLinesInvalidType(t *testing.T) {
	_, err := parseLinesFromResponse(`{"lines":"oops"}`, 1)
	if err == nil {
		t.Fatal("expected error when lines field is not an array")
	}
	if !strings.Contains(err.Error(), `"lines" must be an array`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParseLinesFromResponseErrorsWhenLineItemIsNotString(t *testing.T) {
	_, err := parseLinesFromResponse(`{"lines":["ok",123]}`, 2)
	if err == nil {
		t.Fatal("expected error when line item is not a string")
	}
	if !strings.Contains(err.Error(), `"lines"[1] must be a string`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestOCRSheetLinesRejectsOversizedResponseBody(t *testing.T) {
	root := t.TempDir()
	imagePath := filepath.Join(root, "sheet_0001.png")
	writeTinyPNG(t, imagePath)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, strings.Repeat("x", maxOCRResponseBodyBytes+1))
	}))
	t.Cleanup(server.Close)

	cfg := OCRConfig{
		APIBase:        server.URL,
		APIKey:         "test-key",
		Model:          "test-model",
		TimeoutSeconds: 5,
	}
	_, err := ocrSheetLines(cfg, imagePath, 1)
	if err == nil {
		t.Fatal("expected oversized response error, got nil")
	}
	if !strings.Contains(err.Error(), "response body too large") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestOCRSheetLinesReturnsErrorWhenResponseLinesMissing(t *testing.T) {
	root := t.TempDir()
	imagePath := filepath.Join(root, "sheet_0001.png")
	writeTinyPNG(t, imagePath)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"choices":[{"message":{"content":"{\"not_lines\":[\"a\"]}"}}]}`)
	}))
	t.Cleanup(server.Close)

	cfg := OCRConfig{
		APIBase:        server.URL,
		APIKey:         "test-key",
		Model:          "test-model",
		TimeoutSeconds: 5,
	}
	_, err := ocrSheetLines(cfg, imagePath, 1)
	if err == nil {
		t.Fatal("expected missing lines error, got nil")
	}
	if !strings.Contains(err.Error(), `parse OCR lines: "lines" field is missing`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestOCRSheetLinesBuildRequestFailure(t *testing.T) {
	root := t.TempDir()
	imagePath := filepath.Join(root, "sheet_0001.png")
	writeTinyPNG(t, imagePath)

	cfg := OCRConfig{
		APIBase:        "://bad-url",
		APIKey:         "test-key",
		Model:          "test-model",
		TimeoutSeconds: 5,
	}
	_, err := ocrSheetLines(cfg, imagePath, 1)
	if err == nil {
		t.Fatal("expected request build error, got nil")
	}
	if !strings.Contains(err.Error(), "build OCR request") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestOCRSheetLinesRequestFailure(t *testing.T) {
	root := t.TempDir()
	imagePath := filepath.Join(root, "sheet_0001.png")
	writeTinyPNG(t, imagePath)

	cfg := OCRConfig{
		APIBase:        "http://127.0.0.1:1",
		APIKey:         "test-key",
		Model:          "test-model",
		TimeoutSeconds: 1,
	}
	_, err := ocrSheetLines(cfg, imagePath, 1)
	if err == nil {
		t.Fatal("expected request failure, got nil")
	}
	if !strings.Contains(err.Error(), "OCR API request failed at") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestGetHTTPClientReusesClientByTimeout(t *testing.T) {
	first := getHTTPClient(5)
	second := getHTTPClient(5)
	if first != second {
		t.Fatal("expected same client pointer for same timeout")
	}

	third := getHTTPClient(6)
	if reflect.ValueOf(first).Pointer() == reflect.ValueOf(third).Pointer() {
		t.Fatal("expected different client pointer for different timeout")
	}

	firstTimeout := strconv.Itoa(int(first.Timeout / time.Second))
	thirdTimeout := strconv.Itoa(int(third.Timeout / time.Second))
	if firstTimeout != "5" || thirdTimeout != "6" {
		t.Fatalf("unexpected client timeouts: first=%ss third=%ss", firstTimeout, thirdTimeout)
	}
}
