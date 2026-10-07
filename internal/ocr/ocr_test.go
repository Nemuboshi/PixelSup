package ocr

import (
	"context"
	"encoding/base64"
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
	"sync/atomic"
	"testing"
	"time"
)

func writeTinyPNG(t *testing.T, path string) {
	t.Helper()
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
	return writeConfigWithConcurrency(t, root, defaultOCRMaxConcurrency, extra)
}

func writeConfigWithConcurrency(t *testing.T, root string, maxConcurrency int, extra []string) string {
	t.Helper()
	lines := []string{
		"ocr:",
		"  provider: openai_llm",
		fmt.Sprintf("  max_concurrency: %d", maxConcurrency),
		"openai_llm:",
		"  api_base: https://example.com/v1",
		"  api_key: test-key",
		"  model: test-model",
		"paddle_ocr:",
		"  api_url: https://ndnfp52bz4q410f6.aistudio-app.com/layout-parsing",
		"  token: test-token",
	}
	lines = append(lines, extra...)
	cfg := filepath.Join(root, "config.yaml")
	if err := os.WriteFile(cfg, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return cfg
}

func writePaddleConfig(t *testing.T, root string, apiURL string, extra []string) string {
	return writePaddleConfigWithConcurrency(t, root, apiURL, defaultOCRMaxConcurrency, extra)
}

func writePaddleConfigWithConcurrency(t *testing.T, root string, apiURL string, maxConcurrency int, extra []string) string {
	t.Helper()
	lines := []string{
		"ocr:",
		"  provider: paddle_ocr",
		fmt.Sprintf("  max_concurrency: %d", maxConcurrency),
		"openai_llm:",
		"  api_base: https://example.com/v1",
		"  api_key: test-key",
		"  model: test-model",
		"paddle_ocr:",
		"  api_url: " + apiURL,
		"  token: test-token",
	}
	lines = append(lines, extra...)
	cfg := filepath.Join(root, "paddle-config.yaml")
	if err := os.WriteFile(cfg, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatalf("write paddle config: %v", err)
	}
	return cfg
}

func TestBuildPromptDefaultContainsDigitInstructions(t *testing.T) {
	prompt, err := buildPrompt(OpenAILLMConfig{APIBase: "https://x/v1", APIKey: "k", Model: "m"}, 3)
	if err != nil {
		t.Fatalf("build prompt: %v", err)
	}
	if !strings.Contains(prompt, "0123456789") {
		t.Fatalf("missing separator token in prompt: %q", prompt)
	}
	if !strings.Contains(prompt, "exactly 3 dialogue lines") {
		t.Fatalf("missing expected-count strictness in prompt: %q", prompt)
	}
	if !strings.Contains(prompt, "Do not output a leading or trailing separator") {
		t.Fatalf("missing separator placement constraints in prompt: %q", prompt)
	}
	if strings.Contains(prompt, "Do not include row numbers") {
		t.Fatalf("sheet instructions should be removed: %q", prompt)
	}
}

func TestBuildPromptInvalidTemplateReturnsError(t *testing.T) {
	_, err := buildPrompt(OpenAILLMConfig{
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

func TestParseTextFromResponseTreatsEmptyJSONObjectAsEmpty(t *testing.T) {
	got := parseTextFromResponse(`{}`)
	if got != "" {
		t.Fatalf("expected empty text for empty json object, got %q", got)
	}
}

func TestOpenAIChatCompletionRequestBodyMatchesVisionFormat(t *testing.T) {
	root := t.TempDir()
	imagePath := filepath.Join(root, "sheet_0001.png")
	writeTinyPNG(t, imagePath)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if payload["model"] != "test-model" {
			t.Fatalf("unexpected model: %#v", payload["model"])
		}
		if payload["max_tokens"] != float64(4096) {
			t.Fatalf("unexpected max_tokens: %#v", payload["max_tokens"])
		}
		if _, ok := payload["response_format"]; ok {
			t.Fatalf("response_format must not be sent: %#v", payload["response_format"])
		}
		messages, ok := payload["messages"].([]any)
		if !ok || len(messages) != 1 {
			t.Fatalf("unexpected messages: %#v", payload["messages"])
		}
		msg, ok := messages[0].(map[string]any)
		if !ok {
			t.Fatalf("unexpected message object: %#v", messages[0])
		}
		content, ok := msg["content"].([]any)
		if !ok || len(content) != 2 {
			t.Fatalf("unexpected content payload: %#v", msg["content"])
		}
		textPart, ok := content[0].(map[string]any)
		if !ok || textPart["type"] != "text" {
			t.Fatalf("unexpected text part: %#v", content[0])
		}
		imagePart, ok := content[1].(map[string]any)
		if !ok || imagePart["type"] != "image_url" {
			t.Fatalf("unexpected image part: %#v", content[1])
		}
		imageURL, ok := imagePart["image_url"].(map[string]any)
		if !ok {
			t.Fatalf("unexpected image_url payload: %#v", imagePart["image_url"])
		}
		urlValue, _ := imageURL["url"].(string)
		if !strings.HasPrefix(urlValue, "data:image/png;base64,") {
			t.Fatalf("unexpected image_url.url: %q", urlValue)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"choices":[{"message":{"content":"ok"}}]}`)
	}))
	defer server.Close()

	cfg := OpenAILLMConfig{
		APIBase:        server.URL + "/v1",
		APIKey:         "test-key",
		Model:          "test-model",
		MaxTokens:      4096,
		TimeoutSeconds: 10,
	}
	got, err := openAIChatCompletion(cfg, imagePath, 3)
	if err != nil {
		t.Fatalf("openAIChatCompletion: %v", err)
	}
	if got != "ok" {
		t.Fatalf("unexpected response text: %q", got)
	}
}

func TestLoadOCRConfigValidationErrors(t *testing.T) {
	root := t.TempDir()

	t.Run("missing required fields", func(t *testing.T) {
		cfgPath := filepath.Join(root, "missing-required.yaml")
		yaml := strings.Join([]string{
			"ocr:",
			"  provider: openai_llm",
			"openai_llm:",
			"  api_base: https://example.com/v1",
			"  api_key: test-key",
		}, "\n")
		if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
			t.Fatalf("write config: %v", err)
		}
		_, err := LoadOCRConfig(cfgPath)
		if err == nil || !strings.Contains(err.Error(), "missing required fields for openai_llm") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("negative max retries", func(t *testing.T) {
		cfgPath := filepath.Join(root, "negative-max-retries.yaml")
		yaml := strings.Join([]string{
			"ocr:",
			"  provider: openai_llm",
			"openai_llm:",
			"  api_base: https://example.com/v1",
			"  api_key: test-key",
			"  model: test-model",
			"  max_retries: -1",
			"paddle_ocr:",
			"  api_url: https://ndnfp52bz4q410f6.aistudio-app.com/layout-parsing",
			"  token: test-token",
		}, "\n")
		if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
			t.Fatalf("write config: %v", err)
		}
		_, err := LoadOCRConfig(cfgPath)
		if err == nil || !strings.Contains(err.Error(), "openai_llm.max_retries must be >= 0") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("negative retry backoff", func(t *testing.T) {
		cfgPath := filepath.Join(root, "negative-retry-backoff.yaml")
		yaml := strings.Join([]string{
			"ocr:",
			"  provider: openai_llm",
			"openai_llm:",
			"  api_base: https://example.com/v1",
			"  api_key: test-key",
			"  model: test-model",
			"  retry_backoff_seconds: -0.5",
			"paddle_ocr:",
			"  api_url: https://ndnfp52bz4q410f6.aistudio-app.com/layout-parsing",
			"  token: test-token",
		}, "\n")
		if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
			t.Fatalf("write config: %v", err)
		}
		_, err := LoadOCRConfig(cfgPath)
		if err == nil || !strings.Contains(err.Error(), "openai_llm.retry_backoff_seconds must be >= 0") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("non-positive timeout", func(t *testing.T) {
		cfgPath := filepath.Join(root, "non-positive-timeout.yaml")
		yaml := strings.Join([]string{
			"ocr:",
			"  provider: openai_llm",
			"openai_llm:",
			"  api_base: https://example.com/v1",
			"  api_key: test-key",
			"  model: test-model",
			"  timeout_seconds: 0",
			"paddle_ocr:",
			"  api_url: https://ndnfp52bz4q410f6.aistudio-app.com/layout-parsing",
			"  token: test-token",
		}, "\n")
		if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
			t.Fatalf("write config: %v", err)
		}
		_, err := LoadOCRConfig(cfgPath)
		if err == nil || !strings.Contains(err.Error(), "openai_llm.timeout_seconds must be > 0") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("non-positive max tokens", func(t *testing.T) {
		cfgPath := filepath.Join(root, "non-positive-max-tokens.yaml")
		yaml := strings.Join([]string{
			"ocr:",
			"  provider: openai_llm",
			"openai_llm:",
			"  api_base: https://example.com/v1",
			"  api_key: test-key",
			"  model: test-model",
			"  max_tokens: 0",
			"paddle_ocr:",
			"  api_url: https://ndnfp52bz4q410f6.aistudio-app.com/layout-parsing",
			"  token: test-token",
		}, "\n")
		if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
			t.Fatalf("write config: %v", err)
		}
		_, err := LoadOCRConfig(cfgPath)
		if err == nil || !strings.Contains(err.Error(), "openai_llm.max_tokens must be > 0") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("invalid provider", func(t *testing.T) {
		cfgPath := filepath.Join(root, "invalid-provider.yaml")
		yaml := strings.Join([]string{
			"ocr:",
			"  provider: bad",
			"openai_llm:",
			"  api_base: https://example.com/v1",
			"  api_key: test-key",
			"  model: test-model",
			"paddle_ocr:",
			"  api_url: https://ndnfp52bz4q410f6.aistudio-app.com/layout-parsing",
			"  token: test-token",
		}, "\n")
		if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
			t.Fatalf("write config: %v", err)
		}
		_, err := LoadOCRConfig(cfgPath)
		if err == nil || !strings.Contains(err.Error(), "ocr.provider must be one of") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("paddle provider missing fields", func(t *testing.T) {
		cfgPath := filepath.Join(root, "paddle-missing.yaml")
		yaml := strings.Join([]string{
			"ocr:",
			"  provider: paddle_ocr",
			"openai_llm:",
			"  api_base: https://example.com/v1",
			"  api_key: test-key",
			"  model: test-model",
			"paddle_ocr:",
			"  api_url: https://ndnfp52bz4q410f6.aistudio-app.com/layout-parsing",
		}, "\n")
		if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
			t.Fatalf("write config: %v", err)
		}
		_, err := LoadOCRConfig(cfgPath)
		if err == nil || !strings.Contains(err.Error(), "missing required fields for paddle_ocr") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("ocr non-positive max concurrency", func(t *testing.T) {
		cfgPath := filepath.Join(root, "paddle-bad-concurrency.yaml")
		yaml := strings.Join([]string{
			"ocr:",
			"  provider: paddle_ocr",
			"  max_concurrency: 0",
			"openai_llm:",
			"  api_base: https://example.com/v1",
			"  api_key: test-key",
			"  model: test-model",
			"paddle_ocr:",
			"  api_url: https://ndnfp52bz4q410f6.aistudio-app.com/layout-parsing",
			"  token: test-token",
		}, "\n")
		if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
			t.Fatalf("write config: %v", err)
		}
		_, err := LoadOCRConfig(cfgPath)
		if err == nil || !strings.Contains(err.Error(), "ocr.max_concurrency must be > 0") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestLoadOCRConfigSupportsPaddleProvider(t *testing.T) {
	root := t.TempDir()
	cfgPath := writePaddleConfigWithConcurrency(t, root, "https://paddle.example.test/ocr", 5, nil)

	cfg, err := LoadOCRConfig(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.Provider != ProviderPaddleOCR {
		t.Fatalf("unexpected provider: %s", cfg.Provider)
	}
	if cfg.PaddleOCR.APIURL != "https://paddle.example.test/ocr" {
		t.Fatalf("unexpected paddle api url: %s", cfg.PaddleOCR.APIURL)
	}
	if cfg.MaxConcurrency != 5 {
		t.Fatalf("unexpected ocr numeric config: %+v", cfg)
	}
}

func TestLoadOCRConfigSupportsOpenAIMaxTokens(t *testing.T) {
	root := t.TempDir()
	cfgPath := filepath.Join(root, "openai-max-tokens.yaml")
	yaml := strings.Join([]string{
		"ocr:",
		"  provider: openai_llm",
		"openai_llm:",
		"  api_base: https://example.com/v1",
		"  api_key: test-key",
		"  model: test-model",
		"  max_tokens: 4096",
		"paddle_ocr:",
		"  api_url: https://ndnfp52bz4q410f6.aistudio-app.com/layout-parsing",
		"  token: test-token",
	}, "\n")
	if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	cfg, err := LoadOCRConfig(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.OpenAILLM.MaxTokens != 4096 {
		t.Fatalf("unexpected max tokens: %d", cfg.OpenAILLM.MaxTokens)
	}
}

func TestLoadOCRConfigDefaultsOpenAIMaxTokensWhenUnset(t *testing.T) {
	root := t.TempDir()
	cfgPath := writeConfig(t, root, nil)

	cfg, err := LoadOCRConfig(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.OpenAILLM.MaxTokens != 8192 {
		t.Fatalf("unexpected default max tokens: %d", cfg.OpenAILLM.MaxTokens)
	}
}

func TestLoadOCRConfigFileReadAndParseErrors(t *testing.T) {
	root := t.TempDir()

	t.Run("missing file", func(t *testing.T) {
		_, err := LoadOCRConfig(filepath.Join(root, "does-not-exist.yaml"))
		if err == nil || !strings.Contains(err.Error(), "config file not found") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("invalid yaml", func(t *testing.T) {
		cfgPath := filepath.Join(root, "bad.yaml")
		if err := os.WriteFile(cfgPath, []byte("ocr:\n  api_base: ["), 0o644); err != nil {
			t.Fatalf("write bad yaml: %v", err)
		}
		_, err := LoadOCRConfig(cfgPath)
		if err == nil || !strings.Contains(err.Error(), "parse YAML config") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("read file failure", func(t *testing.T) {
		_, err := LoadOCRConfig(root)
		if err == nil || !strings.Contains(err.Error(), "read config file") {
			t.Fatalf("unexpected error: %v", err)
		}
	})
}

func TestRunOCROnOutputBackfillsTimeline(t *testing.T) {
	root := t.TempDir()
	cfg := writeConfig(t, root, nil)
	writeTinyPNG(t, filepath.Join(root, "sheet_0001.png"))
	writeTinyPNG(t, filepath.Join(root, "sheet_0002.png"))

	mapping := map[string]any{"items": []map[string]any{
		{"cue_index": 1, "start_ms": 0, "end_ms": 1000, "sheet": "sheet_0001.png", "position_in_sheet": 1},
		{"cue_index": 2, "start_ms": 1000, "end_ms": 2000, "sheet": "sheet_0001.png", "position_in_sheet": 2},
		{"cue_index": 3, "start_ms": 2000, "end_ms": 3000, "sheet": "sheet_0002.png", "position_in_sheet": 1},
	}}
	b, _ := json.Marshal(mapping)
	if err := os.WriteFile(filepath.Join(root, "mapping.json"), b, 0o644); err != nil {
		t.Fatalf("write mapping: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "timeline.srt"), nil, 0o644); err != nil {
		t.Fatalf("write timeline: %v", err)
	}

	orig := callOpenAIDigitsTextWithRetry
	callOpenAIDigitsTextWithRetry = func(_ OpenAILLMConfig, imagePath string, expectedCount int) (string, error) {
		switch filepath.Base(imagePath) {
		case "sheet_0001.png":
			if expectedCount != 2 {
				return "", fmt.Errorf("expectedCount=%d", expectedCount)
			}
			return "line a 0123456789 line b", nil
		case "sheet_0002.png":
			if expectedCount != 1 {
				return "", fmt.Errorf("expectedCount=%d", expectedCount)
			}
			return "line c", nil
		default:
			return "", fmt.Errorf("unexpected sheet: %s", imagePath)
		}
	}
	t.Cleanup(func() { callOpenAIDigitsTextWithRetry = orig })

	outPath, err := RunOCROnOutput(root, cfg, false, nil)
	if err != nil {
		t.Fatalf("run ocr: %v", err)
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

func TestRunOCROnOutputReportsProgressAndWritesTimelineFile(t *testing.T) {
	root := t.TempDir()
	cfg := writeConfig(t, root, nil)
	writeTinyPNG(t, filepath.Join(root, "sheet_0001.png"))
	writeTinyPNG(t, filepath.Join(root, "sheet_0002.png"))

	mapping := map[string]any{"items": []map[string]any{
		{"cue_index": 2, "start_ms": 1000, "end_ms": 2000, "sheet": "sheet_0002.png", "position_in_sheet": 1},
		{"cue_index": 1, "start_ms": 0, "end_ms": 1000, "sheet": "sheet_0001.png", "position_in_sheet": 1},
	}}
	b, _ := json.Marshal(mapping)
	if err := os.WriteFile(filepath.Join(root, "mapping.json"), b, 0o644); err != nil {
		t.Fatalf("write mapping: %v", err)
	}

	orig := callOpenAIDigitsTextWithRetry
	callOpenAIDigitsTextWithRetry = func(_ OpenAILLMConfig, imagePath string, expectedCount int) (string, error) {
		switch filepath.Base(imagePath) {
		case "sheet_0001.png":
			if expectedCount != 1 {
				return "", fmt.Errorf("expectedCount=%d", expectedCount)
			}
			return "first line", nil
		case "sheet_0002.png":
			if expectedCount != 1 {
				return "", fmt.Errorf("expectedCount=%d", expectedCount)
			}
			return "second line", nil
		default:
			return "", fmt.Errorf("unexpected sheet: %s", imagePath)
		}
	}
	t.Cleanup(func() { callOpenAIDigitsTextWithRetry = orig })

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
	if filepath.Base(outPath) != "timeline.srt" {
		t.Fatalf("unexpected output file: %s", outPath)
	}
	if len(events) != 3 {
		t.Fatalf("unexpected progress callback count: got=%d want=3", len(events))
	}
	if events[0] != (progressEvent{done: 0, total: 2, sheetName: ""}) {
		t.Fatalf("unexpected first progress event: %+v", events[0])
	}
	if events[1] != (progressEvent{done: 1, total: 2, sheetName: "sheet_0001.png"}) {
		t.Fatalf("unexpected second progress event: %+v", events[1])
	}
	if events[2] != (progressEvent{done: 2, total: 2, sheetName: "sheet_0002.png"}) {
		t.Fatalf("unexpected third progress event: %+v", events[2])
	}
}

func TestRunOCROnOutputMissingMappingAndEmptyMapping(t *testing.T) {
	root := t.TempDir()
	cfg := writeConfig(t, root, nil)

	t.Run("missing mapping.json", func(t *testing.T) {
		_, err := RunOCROnOutput(root, cfg, false, nil)
		if err == nil || !strings.Contains(err.Error(), "mapping.json not found") {
			t.Fatalf("unexpected error: %v", err)
		}
	})

	t.Run("mapping has no items", func(t *testing.T) {
		if err := os.WriteFile(filepath.Join(root, "mapping.json"), []byte(`{"items":[]}`), 0o644); err != nil {
			t.Fatalf("write mapping: %v", err)
		}
		_, err := RunOCROnOutput(root, cfg, false, nil)
		if err == nil || !strings.Contains(err.Error(), "mapping.json contains no items") {
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

	_, err := RunOCROnOutput(root, cfg, false, nil)
	if err == nil || !strings.Contains(err.Error(), "sheet image missing") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunOCROnOutputFallsBackToImageMarkerWhenOCRTextEmpty(t *testing.T) {
	root := t.TempDir()
	cfg := writeConfig(t, root, nil)
	writeTinyPNG(t, filepath.Join(root, "sheet_0001.png"))

	mapping := map[string]any{"items": []map[string]any{
		{"cue_index": 1, "start_ms": 0, "end_ms": 1000, "sheet": "sheet_0001.png", "position_in_sheet": 7},
	}}
	b, _ := json.Marshal(mapping)
	if err := os.WriteFile(filepath.Join(root, "mapping.json"), b, 0o644); err != nil {
		t.Fatalf("write mapping: %v", err)
	}

	orig := callOpenAIDigitsTextWithRetry
	callOpenAIDigitsTextWithRetry = func(_ OpenAILLMConfig, _ string, _ int) (string, error) {
		return "   ", nil
	}
	t.Cleanup(func() { callOpenAIDigitsTextWithRetry = orig })

	outPath, err := RunOCROnOutput(root, cfg, false, nil)
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

func TestRunOCROnOutputDigitsModeSplitsBySeparator(t *testing.T) {
	root := t.TempDir()
	cfg := writeConfig(t, root, nil)
	writeTinyPNG(t, filepath.Join(root, "sheet_0001.png"))

	mapping := map[string]any{"items": []map[string]any{
		{"cue_index": 1, "start_ms": 0, "end_ms": 1000, "sheet": "sheet_0001.png", "position_in_sheet": 1},
		{"cue_index": 2, "start_ms": 1000, "end_ms": 2000, "sheet": "sheet_0001.png", "position_in_sheet": 2},
	}}
	b, _ := json.Marshal(mapping)
	if err := os.WriteFile(filepath.Join(root, "mapping.json"), b, 0o644); err != nil {
		t.Fatalf("write mapping: %v", err)
	}

	orig := callOpenAIDigitsTextWithRetry
	callOpenAIDigitsTextWithRetry = func(_ OpenAILLMConfig, _ string, _ int) (string, error) {
		return "第一行 0123456789 第二行", nil
	}
	t.Cleanup(func() { callOpenAIDigitsTextWithRetry = orig })

	outPath, err := RunOCROnOutput(root, cfg, false, nil)
	if err != nil {
		t.Fatalf("run ocr: %v", err)
	}
	content, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	text := string(content)
	if !strings.Contains(text, "第一行") || !strings.Contains(text, "第二行") {
		t.Fatalf("digits mode split failed: %q", text)
	}
	if strings.Contains(text, "0123456789") {
		t.Fatalf("separator token should be removed from output: %q", text)
	}
}

func TestRunOCROnOutputOpenAIProcessesSheetsConcurrently(t *testing.T) {
	root := t.TempDir()
	cfg := writeConfigWithConcurrency(t, root, 2, nil)
	writeTinyPNG(t, filepath.Join(root, "sheet_0001.png"))
	writeTinyPNG(t, filepath.Join(root, "sheet_0002.png"))

	mapping := map[string]any{"items": []map[string]any{
		{"cue_index": 1, "start_ms": 0, "end_ms": 1000, "sheet": "sheet_0001.png", "position_in_sheet": 1},
		{"cue_index": 2, "start_ms": 1000, "end_ms": 2000, "sheet": "sheet_0002.png", "position_in_sheet": 1},
	}}
	raw, _ := json.Marshal(mapping)
	if err := os.WriteFile(filepath.Join(root, "mapping.json"), raw, 0o644); err != nil {
		t.Fatalf("write mapping: %v", err)
	}

	orig := callOpenAIDigitsTextWithRetry
	var inFlight atomic.Int32
	entered := make(chan string, 2)
	release := make(chan struct{})
	callOpenAIDigitsTextWithRetry = func(_ OpenAILLMConfig, imagePath string, _ int) (string, error) {
		inFlight.Add(1)
		defer inFlight.Add(-1)
		entered <- filepath.Base(imagePath)
		<-release
		return "line", nil
	}
	t.Cleanup(func() { callOpenAIDigitsTextWithRetry = orig })

	done := make(chan error, 1)
	go func() {
		_, err := RunOCROnOutput(root, cfg, false, nil)
		done <- err
	}()

	select {
	case <-entered:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("first openai worker did not start")
	}
	select {
	case <-entered:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("second openai worker did not start concurrently")
	}
	if got := inFlight.Load(); got < 2 {
		t.Fatalf("expected at least two concurrent openai requests, got %d", got)
	}

	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run ocr: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunOCROnOutput did not finish")
	}
}

func TestRunOCROnOutputOpenAIStrictRetriesUntilCountsMatch(t *testing.T) {
	root := t.TempDir()
	cfg := writeConfigWithConcurrency(t, root, 1, nil)
	writeTinyPNG(t, filepath.Join(root, "sheet_0001.png"))

	mapping := map[string]any{"items": []map[string]any{
		{"cue_index": 1, "start_ms": 0, "end_ms": 1000, "sheet": "sheet_0001.png", "position_in_sheet": 1},
		{"cue_index": 2, "start_ms": 1000, "end_ms": 2000, "sheet": "sheet_0001.png", "position_in_sheet": 2},
	}}
	raw, _ := json.Marshal(mapping)
	if err := os.WriteFile(filepath.Join(root, "mapping.json"), raw, 0o644); err != nil {
		t.Fatalf("write mapping: %v", err)
	}

	orig := callOpenAIDigitsTextWithRetry
	var calls atomic.Int32
	callOpenAIDigitsTextWithRetry = func(_ OpenAILLMConfig, _ string, _ int) (string, error) {
		n := calls.Add(1)
		if n < 3 {
			return "only one line", nil
		}
		return "第一行 0123456789 第二行", nil
	}
	t.Cleanup(func() { callOpenAIDigitsTextWithRetry = orig })

	outPath, err := RunOCROnOutput(root, cfg, true, nil)
	if err != nil {
		t.Fatalf("run ocr strict: %v", err)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("unexpected strict retry calls: got=%d want=3", got)
	}
	content, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	text := string(content)
	if !strings.Contains(text, "第一行") || !strings.Contains(text, "第二行") {
		t.Fatalf("timeline missing strict OCR lines: %q", text)
	}
}

func TestRunOCROnOutputOpenAIStrictFailsAfterFiveMismatches(t *testing.T) {
	root := t.TempDir()
	cfg := writeConfigWithConcurrency(t, root, 1, nil)
	writeTinyPNG(t, filepath.Join(root, "sheet_0001.png"))

	mapping := map[string]any{"items": []map[string]any{
		{"cue_index": 1, "start_ms": 0, "end_ms": 1000, "sheet": "sheet_0001.png", "position_in_sheet": 1},
		{"cue_index": 2, "start_ms": 1000, "end_ms": 2000, "sheet": "sheet_0001.png", "position_in_sheet": 2},
	}}
	raw, _ := json.Marshal(mapping)
	if err := os.WriteFile(filepath.Join(root, "mapping.json"), raw, 0o644); err != nil {
		t.Fatalf("write mapping: %v", err)
	}

	orig := callOpenAIDigitsTextWithRetry
	var calls atomic.Int32
	callOpenAIDigitsTextWithRetry = func(_ OpenAILLMConfig, _ string, _ int) (string, error) {
		calls.Add(1)
		return "still one line", nil
	}
	t.Cleanup(func() { callOpenAIDigitsTextWithRetry = orig })

	_, err := RunOCROnOutput(root, cfg, true, nil)
	if err == nil || !strings.Contains(err.Error(), "strict OCR split count mismatch") {
		t.Fatalf("unexpected strict mismatch error: %v", err)
	}
	if got := calls.Load(); got != strictSplitMaxAttempts {
		t.Fatalf("unexpected strict retry calls: got=%d want=%d", got, strictSplitMaxAttempts)
	}
	outPath := filepath.Join(root, "timeline.srt")
	if _, statErr := os.Stat(outPath); statErr != nil {
		t.Fatalf("timeline should still be written on strict mismatch: %v", statErr)
	}
	content, readErr := os.ReadFile(outPath)
	if readErr != nil {
		t.Fatalf("read output: %v", readErr)
	}
	if !strings.Contains(string(content), "still one line") {
		t.Fatalf("strict mismatch should backfill last attempt result, got: %q", string(content))
	}
}

func TestRunOCROnOutputPaddleStrictRetriesUntilCountsMatch(t *testing.T) {
	root := t.TempDir()
	cfg := writePaddleConfigWithConcurrency(t, root, "https://paddle.example.test/ocr", 1, nil)
	writeTinyPNG(t, filepath.Join(root, "sheet_0001.png"))

	mapping := map[string]any{"items": []map[string]any{
		{"cue_index": 1, "start_ms": 0, "end_ms": 1000, "sheet": "sheet_0001.png", "position_in_sheet": 1},
		{"cue_index": 2, "start_ms": 1000, "end_ms": 2000, "sheet": "sheet_0001.png", "position_in_sheet": 2},
	}}
	raw, _ := json.Marshal(mapping)
	if err := os.WriteFile(filepath.Join(root, "mapping.json"), raw, 0o644); err != nil {
		t.Fatalf("write mapping: %v", err)
	}

	orig := callPaddleSheetTextWithRetry
	var calls atomic.Int32
	callPaddleSheetTextWithRetry = func(_ PaddleOCRConfig, _ string, _ int) (string, error) {
		n := calls.Add(1)
		if n < 3 {
			return "only one line", nil
		}
		return "第一行 0123456789 第二行", nil
	}
	t.Cleanup(func() { callPaddleSheetTextWithRetry = orig })

	outPath, err := RunOCROnOutput(root, cfg, true, nil)
	if err != nil {
		t.Fatalf("run paddle strict: %v", err)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("unexpected paddle strict retry calls: got=%d want=3", got)
	}
	content, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	text := string(content)
	if !strings.Contains(text, "第一行") || !strings.Contains(text, "第二行") {
		t.Fatalf("timeline missing paddle strict OCR lines: %q", text)
	}
}

func TestRunOCROnOutputStrictMismatchContinuesOtherSheets(t *testing.T) {
	root := t.TempDir()
	cfg := writeConfigWithConcurrency(t, root, 2, nil)
	writeTinyPNG(t, filepath.Join(root, "sheet_0001.png"))
	writeTinyPNG(t, filepath.Join(root, "sheet_0002.png"))

	mapping := map[string]any{"items": []map[string]any{
		{"cue_index": 1, "start_ms": 0, "end_ms": 1000, "sheet": "sheet_0001.png", "position_in_sheet": 1},
		{"cue_index": 2, "start_ms": 1000, "end_ms": 2000, "sheet": "sheet_0001.png", "position_in_sheet": 2},
		{"cue_index": 3, "start_ms": 2000, "end_ms": 3000, "sheet": "sheet_0002.png", "position_in_sheet": 1},
	}}
	raw, _ := json.Marshal(mapping)
	if err := os.WriteFile(filepath.Join(root, "mapping.json"), raw, 0o644); err != nil {
		t.Fatalf("write mapping: %v", err)
	}

	orig := callOpenAIDigitsTextWithRetry
	var sheet2Calls atomic.Int32
	callOpenAIDigitsTextWithRetry = func(_ OpenAILLMConfig, imagePath string, _ int) (string, error) {
		switch filepath.Base(imagePath) {
		case "sheet_0001.png":
			return "always wrong", nil
		case "sheet_0002.png":
			sheet2Calls.Add(1)
			return "第三行", nil
		default:
			return "", fmt.Errorf("unexpected sheet: %s", imagePath)
		}
	}
	t.Cleanup(func() { callOpenAIDigitsTextWithRetry = orig })

	outPath, err := RunOCROnOutput(root, cfg, true, nil)
	if err == nil || !strings.Contains(err.Error(), "strict OCR mismatches") {
		t.Fatalf("expected strict mismatch summary error, got: %v", err)
	}
	if got := sheet2Calls.Load(); got == 0 {
		t.Fatalf("expected strict mode to continue and process sheet_0002")
	}
	content, readErr := os.ReadFile(outPath)
	if readErr != nil {
		t.Fatalf("read output: %v", readErr)
	}
	text := string(content)
	if !strings.Contains(text, "第三行") {
		t.Fatalf("expected successful sheet text in timeline, got: %q", text)
	}
}

func TestDigitsTextWithRetryRetriesTransientFailures(t *testing.T) {
	cfg := OpenAILLMConfig{MaxRetries: 2, RetryBackoffSeconds: 0}
	origCall := callOpenAIDigitsText
	origSleep := sleepFn
	defer func() {
		callOpenAIDigitsText = origCall
		sleepFn = origSleep
	}()
	sleepFn = func(_ time.Duration) {}

	calls := 0
	callOpenAIDigitsText = func(_ OpenAILLMConfig, _ string, _ int) (string, error) {
		calls++
		if calls == 1 {
			return "", context.DeadlineExceeded
		}
		return "ok", nil
	}

	got, err := ocrOpenAIDigitsTextWithRetry(cfg, "sheet_0001.png", 1)
	if err != nil || got != "ok" {
		t.Fatalf("retry wrapper returned (%q, %v)", got, err)
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

func TestPaddleSheetTextReconstructsLinesFromPrunedTextBoxes(t *testing.T) {
	root := t.TempDir()
	imagePath := filepath.Join(root, "sheet_0001.png")
	writeTinyPNG(t, imagePath)

	mux := http.NewServeMux()
	mux.HandleFunc("/ocr", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		if got := r.Header.Get("Authorization"); got != "token test-token" {
			t.Fatalf("unexpected auth header: %q", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Fatalf("unexpected content-type: %q", got)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if payload["fileType"] != float64(1) {
			t.Fatalf("unexpected fileType: %#v", payload["fileType"])
		}
		if payload["useTextlineOrientation"] != false {
			t.Fatalf("unexpected useTextlineOrientation: %#v", payload["useTextlineOrientation"])
		}
		fileData, ok := payload["file"].(string)
		if !ok || fileData == "" {
			t.Fatalf("missing file payload: %#v", payload["file"])
		}
		if _, err := base64.StdEncoding.DecodeString(fileData); err != nil {
			t.Fatalf("invalid base64 payload: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"result":{"ocrResults":[{"prunedResult":{"rec_texts":["第一","行","0123456789","第二行"],"rec_boxes":[[10,10,30,20],[35,11,55,21],[10,40,100,52],[10,70,80,82]],"dt_polys":[[[10,10],[30,10],[30,20],[10,20]],[[35,11],[55,11],[55,21],[35,21]],[[10,40],[100,40],[100,52],[10,52]],[[10,70],[80,70],[80,82],[10,82]]]}}]}}`)
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	cfg := PaddleOCRConfig{
		APIURL: server.URL + "/ocr",
		Token:  "test-token",
	}

	text, err := paddleSheetText(cfg, imagePath, 2)
	if err != nil {
		t.Fatalf("paddle sheet text: %v", err)
	}
	if text != "第一行\n\n0123456789\n\n第二行" {
		t.Fatalf("unexpected paddle text ordering: %q", text)
	}
}

func TestPaddleSheetTextReturnsAPIError(t *testing.T) {
	root := t.TempDir()
	imagePath := filepath.Join(root, "sheet_0001.png")
	writeTinyPNG(t, imagePath)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"error":"bad image"}`)
	}))
	defer server.Close()

	cfg := PaddleOCRConfig{
		APIURL: server.URL + "/ocr",
		Token:  "test-token",
	}

	_, err := paddleSheetText(cfg, imagePath, 1)
	if err == nil || !strings.Contains(err.Error(), "OCR API request failed (502)") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPaddleSheetTextRejectsInvalidResponseJSON(t *testing.T) {
	root := t.TempDir()
	imagePath := filepath.Join(root, "sheet_0001.png")
	writeTinyPNG(t, imagePath)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, "not-json")
	}))
	defer server.Close()

	cfg := PaddleOCRConfig{
		APIURL: server.URL + "/ocr",
		Token:  "test-token",
	}

	_, err := paddleSheetText(cfg, imagePath, 1)
	if err == nil || !strings.Contains(err.Error(), "decode paddle OCR response") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPaddleSheetTextRejectsMissingPrunedResultGeometry(t *testing.T) {
	root := t.TempDir()
	imagePath := filepath.Join(root, "sheet_0001.png")
	writeTinyPNG(t, imagePath)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"result":{"ocrResults":[{"prunedResult":{"rec_texts":["第一行"]}}]}}`)
	}))
	defer server.Close()

	cfg := PaddleOCRConfig{
		APIURL: server.URL + "/ocr",
		Token:  "test-token",
	}

	_, err := paddleSheetText(cfg, imagePath, 1)
	if err == nil || !strings.Contains(err.Error(), "missing prunedResult.rec_boxes") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunOCROnOutputPaddleAlignsAndFallsBack(t *testing.T) {
	root := t.TempDir()
	cfg := writePaddleConfig(t, root, "https://paddle.example.test/ocr", nil)
	writeTinyPNG(t, filepath.Join(root, "sheet_0001.png"))
	writeTinyPNG(t, filepath.Join(root, "sheet_0002.png"))

	mapping := map[string]any{"items": []map[string]any{
		{"cue_index": 1, "start_ms": 0, "end_ms": 1000, "sheet": "sheet_0001.png", "position_in_sheet": 1},
		{"cue_index": 2, "start_ms": 1000, "end_ms": 2000, "sheet": "sheet_0001.png", "position_in_sheet": 2},
		{"cue_index": 3, "start_ms": 2000, "end_ms": 3000, "sheet": "sheet_0002.png", "position_in_sheet": 1},
	}}
	raw, _ := json.Marshal(mapping)
	if err := os.WriteFile(filepath.Join(root, "mapping.json"), raw, 0o644); err != nil {
		t.Fatalf("write mapping: %v", err)
	}

	orig := callPaddleSheetTextWithRetry
	callPaddleSheetTextWithRetry = func(_ PaddleOCRConfig, imagePath string, expectedCount int) (string, error) {
		switch filepath.Base(imagePath) {
		case "sheet_0001.png":
			if expectedCount != 2 {
				return "", fmt.Errorf("expectedCount=%d", expectedCount)
			}
			return "第一行", nil
		case "sheet_0002.png":
			return "0123456789", nil
		default:
			return "", fmt.Errorf("unexpected sheet: %s", imagePath)
		}
	}
	t.Cleanup(func() { callPaddleSheetTextWithRetry = orig })

	outPath, err := RunOCROnOutput(root, cfg, false, nil)
	if err != nil {
		t.Fatalf("run ocr: %v", err)
	}
	content, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	text := string(content)
	if !strings.Contains(text, "第一行") {
		t.Fatalf("timeline missing recognized text: %q", text)
	}
	if !strings.Contains(text, "[img:sheet_0001.png#02]") || !strings.Contains(text, "[img:sheet_0002.png#01]") {
		t.Fatalf("timeline missing fallback markers: %q", text)
	}
}

func TestRunOCROnOutputPaddleProcessesSheetsConcurrently(t *testing.T) {
	root := t.TempDir()
	cfg := writePaddleConfigWithConcurrency(t, root, "https://paddle.example.test/ocr", 2, nil)
	writeTinyPNG(t, filepath.Join(root, "sheet_0001.png"))
	writeTinyPNG(t, filepath.Join(root, "sheet_0002.png"))

	mapping := map[string]any{"items": []map[string]any{
		{"cue_index": 1, "start_ms": 0, "end_ms": 1000, "sheet": "sheet_0001.png", "position_in_sheet": 1},
		{"cue_index": 2, "start_ms": 1000, "end_ms": 2000, "sheet": "sheet_0002.png", "position_in_sheet": 1},
	}}
	raw, _ := json.Marshal(mapping)
	if err := os.WriteFile(filepath.Join(root, "mapping.json"), raw, 0o644); err != nil {
		t.Fatalf("write mapping: %v", err)
	}

	orig := callPaddleSheetTextWithRetry
	var inFlight atomic.Int32
	entered := make(chan string, 2)
	release := make(chan struct{})
	callPaddleSheetTextWithRetry = func(_ PaddleOCRConfig, imagePath string, _ int) (string, error) {
		inFlight.Add(1)
		defer inFlight.Add(-1)
		entered <- filepath.Base(imagePath)
		<-release
		return "line", nil
	}
	t.Cleanup(func() { callPaddleSheetTextWithRetry = orig })

	done := make(chan error, 1)
	go func() {
		_, err := RunOCROnOutput(root, cfg, false, nil)
		done <- err
	}()

	select {
	case <-entered:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("first paddle worker did not start")
	}
	select {
	case <-entered:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("second paddle worker did not start concurrently")
	}
	if got := inFlight.Load(); got < 2 {
		t.Fatalf("expected at least two concurrent paddle requests, got %d", got)
	}

	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run ocr: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunOCROnOutput did not finish")
	}
}

func TestRunOCROnOutputPaddleReportsProgressBeforeAllJobsAreScheduled(t *testing.T) {
	root := t.TempDir()
	cfg := writePaddleConfigWithConcurrency(t, root, "https://paddle.example.test/ocr", 2, nil)
	for i := 1; i <= 4; i++ {
		writeTinyPNG(t, filepath.Join(root, fmt.Sprintf("sheet_%04d.png", i)))
	}

	items := make([]map[string]any, 0, 4)
	for i := 1; i <= 4; i++ {
		items = append(items, map[string]any{
			"cue_index":         i,
			"start_ms":          (i - 1) * 1000,
			"end_ms":            i * 1000,
			"sheet":             fmt.Sprintf("sheet_%04d.png", i),
			"position_in_sheet": 1,
		})
	}
	raw, _ := json.Marshal(map[string]any{"items": items})
	if err := os.WriteFile(filepath.Join(root, "mapping.json"), raw, 0o644); err != nil {
		t.Fatalf("write mapping: %v", err)
	}

	orig := callPaddleSheetTextWithRetry
	releaseBySheet := map[string]chan struct{}{
		"sheet_0001.png": make(chan struct{}),
		"sheet_0002.png": make(chan struct{}),
		"sheet_0003.png": make(chan struct{}),
		"sheet_0004.png": make(chan struct{}),
	}
	entered := make(chan string, 4)
	callPaddleSheetTextWithRetry = func(_ PaddleOCRConfig, imagePath string, _ int) (string, error) {
		name := filepath.Base(imagePath)
		entered <- name
		<-releaseBySheet[name]
		return "line", nil
	}
	t.Cleanup(func() { callPaddleSheetTextWithRetry = orig })

	progressEvents := make(chan struct {
		done      int
		total     int
		sheetName string
	}, 8)
	done := make(chan error, 1)
	go func() {
		_, err := RunOCROnOutput(root, cfg, false, func(done, total int, sheetName string) {
			progressEvents <- struct {
				done      int
				total     int
				sheetName string
			}{done: done, total: total, sheetName: sheetName}
		})
		done <- err
	}()

	firstEvent := <-progressEvents
	if firstEvent.done != 0 || firstEvent.total != 4 {
		t.Fatalf("unexpected initial progress event: %+v", firstEvent)
	}

	started := map[string]bool{}
	for len(started) < 2 {
		select {
		case name := <-entered:
			started[name] = true
		case <-time.After(500 * time.Millisecond):
			t.Fatal("expected first two paddle workers to start")
		}
	}

	close(releaseBySheet["sheet_0001.png"])

	select {
	case event := <-progressEvents:
		if event.done != 1 || event.total != 4 || event.sheetName != "sheet_0001.png" {
			t.Fatalf("unexpected incremental progress event: %+v", event)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected progress after first worker completed, before all jobs were scheduled")
	}

	for _, name := range []string{"sheet_0002.png", "sheet_0003.png", "sheet_0004.png"} {
		close(releaseBySheet[name])
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run ocr: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunOCROnOutput did not finish")
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
