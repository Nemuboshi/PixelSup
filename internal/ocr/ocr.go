package ocr

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
	"pixelsup-go/internal/timeline"
)

// OCRConfig stores API and runtime controls for OCR requests.
// Field names map 1:1 to the Python configuration contract.
type OCRConfig struct {
	APIBase             string
	APIKey              string
	Model               string
	TimeoutSeconds      int
	MaxRetries          int
	RetryBackoffSeconds float64
	PromptTemplate      string
}

// ProgressFunc reports per-sheet OCR progress while RunOCROnOutput iterates sheets.
type ProgressFunc func(done, total int, sheetName string)

const defaultTimeoutSeconds = 120
const defaultMaxRetries = 2
const defaultRetryBackoffSeconds = 1.0
const maxOCRResponseBytes = 8 << 20
const maxOCRResponseBodyBytes = maxOCRResponseBytes

// ocrHTTPStatusError keeps HTTP status details so retry logic can differentiate
// deterministic client failures (4xx) from transient provider failures (429/5xx).
type ocrHTTPStatusError struct {
	StatusCode int
	URL        string
	Detail     string
}

func (e *ocrHTTPStatusError) Error() string {
	return fmt.Sprintf("OCR API request failed (%d) at %s: %s", e.StatusCode, e.URL, e.Detail)
}

// ocrSchemaError marks response payload/schema issues. Retrying identical inputs on
// these failures is usually wasted work because provider output shape is already invalid.
type ocrSchemaError struct {
	Message string
}

func (e *ocrSchemaError) Error() string {
	return e.Message
}

// DefaultJAOCRPrompt mirrors the Python prompt contract used for Japanese dialogue OCR.
const DefaultJAOCRPrompt = "" +
	"You are an OCR assistant. The image contains Japanese dialogue lines in a table.\n" +
	"Transcribe all original text with high precision.\n" +
	"There are exactly {expected_count} rows.\n" +
	"Rules:\n" +
	"1) Left side is row number, right side is content.\n" +
	"2) Keep row order strictly aligned with row numbers, top to bottom.\n" +
	"3) Do not include row numbers in output text.\n" +
	"4) Keep each returned item as a single-line string. If original content wraps, use literal \\\\N to represent line breaks.\n" +
	"   IMPORTANT: output must be valid JSON. Write escaped backslash as \\\\\\\\N in JSON strings (NOT raw \\N).\n" +
	"5) Preserve punctuation exactly.\n" +
	"6) If small ruby text exists, annotate it in full-width parentheses (...).\n" +
	"7) Be strict about small kana vs normal kana.\n" +
	"Output JSON only: {{\"lines\":[\"...\", \"...\"]}} with exactly {expected_count} items.\n" +
	"Example valid JSON item: \"line_A\\\\\\\\Nline_B\""

var stringLiteralPattern = regexp.MustCompile(`"((?:\\.|[^"\\])*)"`)

var callSheetLines = ocrSheetLines
var callSheetLinesWithRetry = ocrSheetLinesWithRetry
var sleepFn = func(d time.Duration) { time.Sleep(d) }
var sharedTransport = &http.Transport{
	Proxy:             http.ProxyFromEnvironment,
	MaxIdleConns:      100,
	IdleConnTimeout:   90 * time.Second,
	ForceAttemptHTTP2: true,
}
var httpClientCache sync.Map // map[int]*http.Client keyed by timeout seconds

// LoadOCRConfig reads OCR settings from a YAML file and applies Python-equivalent defaults.
func LoadOCRConfig(path string) (OCRConfig, error) {
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return OCRConfig{}, fmt.Errorf("config file not found: %s", path)
		}
		return OCRConfig{}, fmt.Errorf("stat config file %s: %w", path, err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return OCRConfig{}, fmt.Errorf("read config file %s: %w", path, err)
	}

	cfg := OCRConfig{
		TimeoutSeconds:      defaultTimeoutSeconds,
		MaxRetries:          defaultMaxRetries,
		RetryBackoffSeconds: defaultRetryBackoffSeconds,
	}

	var doc struct {
		OCR struct {
			APIBase             string   `yaml:"api_base"`
			APIKey              string   `yaml:"api_key"`
			Model               string   `yaml:"model"`
			TimeoutSeconds      *int     `yaml:"timeout_seconds"`
			MaxRetries          *int     `yaml:"max_retries"`
			RetryBackoffSeconds *float64 `yaml:"retry_backoff_seconds"`
			PromptTemplate      *string  `yaml:"prompt_template"`
		} `yaml:"ocr"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return OCRConfig{}, fmt.Errorf("parse YAML config %s: %w", path, err)
	}

	cfg.APIBase = strings.TrimRight(strings.TrimSpace(doc.OCR.APIBase), "/")
	cfg.APIKey = strings.TrimSpace(doc.OCR.APIKey)
	cfg.Model = strings.TrimSpace(doc.OCR.Model)
	if doc.OCR.TimeoutSeconds != nil {
		cfg.TimeoutSeconds = *doc.OCR.TimeoutSeconds
	}
	if doc.OCR.MaxRetries != nil {
		cfg.MaxRetries = *doc.OCR.MaxRetries
	}
	if doc.OCR.RetryBackoffSeconds != nil {
		cfg.RetryBackoffSeconds = *doc.OCR.RetryBackoffSeconds
	}
	if doc.OCR.PromptTemplate != nil {
		cfg.PromptTemplate = *doc.OCR.PromptTemplate
	}

	if cfg.APIBase == "" || cfg.APIKey == "" || cfg.Model == "" {
		return OCRConfig{}, errors.New("ocr_config.yaml missing required fields: ocr.api_base, ocr.api_key, ocr.model")
	}
	if cfg.MaxRetries < 0 {
		return OCRConfig{}, errors.New("ocr.max_retries must be >= 0")
	}
	if cfg.RetryBackoffSeconds < 0 {
		return OCRConfig{}, errors.New("ocr.retry_backoff_seconds must be >= 0")
	}
	if cfg.TimeoutSeconds <= 0 {
		return OCRConfig{}, errors.New("ocr.timeout_seconds must be > 0")
	}

	return cfg, nil
}

func dataURI(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read image %s: %w", path, err)
	}
	b64 := base64.StdEncoding.EncodeToString(raw)
	return "data:image/png;base64," + b64, nil
}

func extractTextContent(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case []any:
		parts := make([]string, 0, len(v))
		for _, item := range v {
			obj, ok := item.(map[string]any)
			if !ok {
				continue
			}
			text, ok := obj["text"]
			if !ok {
				continue
			}
			parts = append(parts, fmt.Sprint(text))
		}
		return strings.Join(parts, "\n")
	default:
		return fmt.Sprint(content)
	}
}

// alignLineCount reproduces Python line-count alignment semantics.
// Missing items are padded with empty strings, while overflow is merged into the final slot.
func alignLineCount(lines []string, expectedCount int) []string {
	out := make([]string, len(lines))
	for i, line := range lines {
		out[i] = strings.TrimSpace(line)
	}

	if len(out) < expectedCount {
		padded := make([]string, expectedCount)
		copy(padded, out)
		return padded
	}
	if len(out) <= expectedCount {
		return out
	}

	switch expectedCount {
	case 0:
		return []string{}
	case 1:
		return []string{strings.Join(out, " ")}
	default:
		merged := append([]string{}, out[:expectedCount-1]...)
		merged = append(merged, strings.Join(out[expectedCount-1:], " "))
		return merged
	}
}

// parseLinesFromResponse decodes OCR JSON and keeps output aligned with expected rows.
//
// Providers occasionally return truncated JSON near the tail of the response. The fallback
// scanner deliberately extracts JSON string literals around the "lines" key so we can still
// salvage partial results instead of discarding the whole OCR attempt.
func parseLinesFromResponse(contentText string, expectedCount int) ([]string, error) {
	text := strings.TrimSpace(contentText)
	if strings.HasPrefix(text, "```") {
		text = strings.Trim(text, "`")
		text = strings.TrimSpace(text)
		if strings.HasPrefix(text, "json") {
			text = strings.TrimSpace(text[4:])
		}
	}

	var parseErr error
	allowFallback := false
	var envelope struct {
		Lines json.RawMessage `json:"lines"`
	}
	if err := json.Unmarshal([]byte(text), &envelope); err == nil {
		if len(envelope.Lines) == 0 {
			parseErr = errors.New(`"lines" field is missing`)
		} else {
			var rawLines []any
			if err := json.Unmarshal(envelope.Lines, &rawLines); err == nil {
				lines := make([]string, 0, len(rawLines))
				for i, rawLine := range rawLines {
					s, ok := rawLine.(string)
					if !ok {
						return nil, fmt.Errorf(`"lines"[%d] must be a string`, i)
					}
					lines = append(lines, s)
				}
				return alignLineCount(lines, expectedCount), nil
			}
			parseErr = errors.New(`"lines" must be an array`)
		}
	} else {
		parseErr = err
		allowFallback = true
	}
	if !allowFallback {
		return nil, parseErr
	}

	scan := text
	if idx := strings.Index(text, `"lines"`); idx >= 0 {
		scan = text[idx:]
	}

	matches := stringLiteralPattern.FindAllStringSubmatch(scan, -1)
	lines := make([]string, 0, len(matches))
	for _, match := range matches {
		if len(match) < 2 {
			continue
		}
		decoded, err := strconv.Unquote(`"` + match[1] + `"`)
		if err != nil {
			continue
		}
		lines = append(lines, strings.TrimSpace(decoded))
	}
	if len(lines) > 0 && lines[0] == "lines" {
		lines = lines[1:]
	}
	if len(lines) == 0 {
		return nil, parseErr
	}
	return alignLineCount(lines, expectedCount), nil
}

func renderTemplate(template string, expectedCount int) (string, error) {
	const leftSentinel = "\x00LEFT_BRACE\x00"
	const rightSentinel = "\x00RIGHT_BRACE\x00"

	s := strings.ReplaceAll(template, "{{", leftSentinel)
	s = strings.ReplaceAll(s, "}}", rightSentinel)
	s = strings.ReplaceAll(s, "{expected_count}", strconv.Itoa(expectedCount))
	if strings.ContainsAny(s, "{}") {
		return "", errors.New("invalid ocr.prompt_template. It must support {expected_count}")
	}
	s = strings.ReplaceAll(s, leftSentinel, "{")
	s = strings.ReplaceAll(s, rightSentinel, "}")
	return s, nil
}

func buildPrompt(config OCRConfig, expectedCount int) (string, error) {
	template := strings.TrimSpace(config.PromptTemplate)
	if template == "" {
		template = DefaultJAOCRPrompt
	}
	return renderTemplate(template, expectedCount)
}

func ocrSheetLines(config OCRConfig, imagePath string, expectedCount int) ([]string, error) {
	prompt, err := buildPrompt(config, expectedCount)
	if err != nil {
		return nil, err
	}
	uri, err := dataURI(imagePath)
	if err != nil {
		return nil, err
	}

	payload := map[string]any{
		"model":       config.Model,
		"temperature": 0,
		"messages": []map[string]any{
			{
				"role": "user",
				"content": []map[string]any{
					{"type": "text", "text": prompt},
					{"type": "image_url", "image_url": map[string]any{"url": uri}},
				},
			},
		},
		"response_format": map[string]any{"type": "json_object"},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal OCR request payload: %w", err)
	}

	url := config.APIBase + "/chat/completions"
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(config.TimeoutSeconds)*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build OCR request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+config.APIKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := getHTTPClient(config.TimeoutSeconds).Do(req)
	if err != nil {
		return nil, fmt.Errorf("OCR API request failed at %s: %w", url, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxOCRResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read OCR API response: %w", err)
	}
	if len(respBody) > maxOCRResponseBytes {
		return nil, &ocrSchemaError{
			Message: fmt.Sprintf("OCR API response body too large (> %d bytes)", maxOCRResponseBytes),
		}
	}
	if resp.StatusCode >= 400 {
		detail := strings.TrimSpace(string(respBody))
		if len(detail) > 500 {
			detail = detail[:500]
		}
		return nil, &ocrHTTPStatusError{
			StatusCode: resp.StatusCode,
			URL:        url,
			Detail:     detail,
		}
	}

	var data struct {
		Choices []struct {
			Message struct {
				Content any `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &data); err != nil {
		return nil, &ocrSchemaError{
			Message: fmt.Sprintf("decode OCR response JSON: %v", err),
		}
	}
	if len(data.Choices) == 0 {
		return nil, &ocrSchemaError{
			Message: "decode OCR response JSON: choices is empty",
		}
	}

	contentText := extractTextContent(data.Choices[0].Message.Content)
	lines, err := parseLinesFromResponse(contentText, expectedCount)
	if err != nil {
		return nil, &ocrSchemaError{
			Message: fmt.Sprintf("parse OCR lines: %v", err),
		}
	}
	return lines, nil
}

// shouldRetryOCRError classifies OCR failures into retryable and non-retryable buckets.
//
// Retryable:
// - Network and timeout failures (context deadline and net timeout errors)
// - Provider-side throttling/transient HTTP failures (429 and 5xx)
//
// Non-retryable:
// - Deterministic client HTTP failures (other 4xx)
// - Response schema/format errors where replaying the same request is unlikely to help
func shouldRetryOCRError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	if errors.Is(err, context.Canceled) {
		return false
	}

	var statusErr *ocrHTTPStatusError
	if errors.As(err, &statusErr) {
		return statusErr.StatusCode == http.StatusTooManyRequests || statusErr.StatusCode >= http.StatusInternalServerError
	}

	var schemaErr *ocrSchemaError
	if errors.As(err, &schemaErr) {
		return false
	}

	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	return false
}

// ocrSheetLinesWithRetry applies exponential backoff on broad failures because OCR providers
// frequently fail with transient transport and provider-side errors. Keeping retries here means
// mapping/timeline generation logic stays deterministic while request resilience remains isolated.
func ocrSheetLinesWithRetry(config OCRConfig, imagePath string, expectedCount int) ([]string, error) {
	attempts := config.MaxRetries + 1
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		lines, err := callSheetLines(config, imagePath, expectedCount)
		if err == nil {
			return lines, nil
		}
		lastErr = err
		if !shouldRetryOCRError(err) {
			break
		}
		if attempt >= attempts-1 {
			break
		}
		if config.RetryBackoffSeconds > 0 {
			backoffSeconds := config.RetryBackoffSeconds * math.Pow(2, float64(attempt))
			sleepFn(time.Duration(backoffSeconds * float64(time.Second)))
		}
	}
	return nil, fmt.Errorf("OCR failed for sheet %s after %d attempts: %w", filepath.Base(imagePath), attempts, lastErr)
}

func getHTTPClient(timeoutSeconds int) *http.Client {
	if timeoutSeconds <= 0 {
		timeoutSeconds = defaultTimeoutSeconds
	}
	if cached, ok := httpClientCache.Load(timeoutSeconds); ok {
		return cached.(*http.Client)
	}
	client := &http.Client{
		Transport: sharedTransport,
		Timeout:   time.Duration(timeoutSeconds) * time.Second,
	}
	actual, _ := httpClientCache.LoadOrStore(timeoutSeconds, client)
	return actual.(*http.Client)
}

type mappingPayload struct {
	Items []mappingItem `json:"items"`
}

type mappingItem struct {
	CueIndex        int    `json:"cue_index"`
	StartMS         int    `json:"start_ms"`
	EndMS           int    `json:"end_ms"`
	Sheet           string `json:"sheet"`
	PositionInSheet int    `json:"position_in_sheet"`
}

// RunOCROnOutput reads mapping.json, performs OCR per sheet, and rewrites timeline text.
func RunOCROnOutput(outputDir, configPath string, overwriteTimeline bool, progressCB ProgressFunc) (string, error) {
	config, err := LoadOCRConfig(configPath)
	if err != nil {
		return "", err
	}

	mapPath := filepath.Join(outputDir, "mapping.json")
	if _, err := os.Stat(mapPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("mapping.json not found in %s", outputDir)
		}
		return "", fmt.Errorf("stat mapping.json: %w", err)
	}

	rawMapping, err := os.ReadFile(mapPath)
	if err != nil {
		return "", fmt.Errorf("read mapping.json: %w", err)
	}
	var payload mappingPayload
	if err := json.Unmarshal(rawMapping, &payload); err != nil {
		return "", fmt.Errorf("parse mapping.json: %w", err)
	}
	if len(payload.Items) == 0 {
		return "", errors.New("mapping.json contains no items")
	}

	bySheet := make(map[string][]mappingItem)
	for _, item := range payload.Items {
		bySheet[item.Sheet] = append(bySheet[item.Sheet], item)
	}
	for sheet := range bySheet {
		sort.Slice(bySheet[sheet], func(i, j int) bool {
			return bySheet[sheet][i].PositionInSheet < bySheet[sheet][j].PositionInSheet
		})
	}

	sheetNames := make([]string, 0, len(bySheet))
	for sheet := range bySheet {
		sheetNames = append(sheetNames, sheet)
	}
	sort.Strings(sheetNames)

	cueTexts := make(map[int]string, len(payload.Items))
	for idx, sheetName := range sheetNames {
		sheetPath := filepath.Join(outputDir, sheetName)
		if _, err := os.Stat(sheetPath); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return "", fmt.Errorf("sheet image missing: %s", sheetPath)
			}
			return "", fmt.Errorf("stat sheet image %s: %w", sheetPath, err)
		}

		sheetItems := bySheet[sheetName]
		lines, err := callSheetLinesWithRetry(config, sheetPath, len(sheetItems))
		if err != nil {
			return "", err
		}
		for i, item := range sheetItems {
			if i >= len(lines) {
				break
			}
			cueTexts[item.CueIndex] = strings.TrimSpace(lines[i])
		}

		if progressCB != nil {
			progressCB(idx+1, len(sheetNames), sheetName)
		}
	}

	items := append([]mappingItem{}, payload.Items...)
	sort.Slice(items, func(i, j int) bool {
		return items[i].CueIndex < items[j].CueIndex
	})

	srtLines := make([]string, 0, len(items)*4)
	for i, item := range items {
		text := strings.TrimSpace(cueTexts[item.CueIndex])
		if text == "" {
			text = fmt.Sprintf("[img:%s#%02d]", item.Sheet, item.PositionInSheet)
		}

		srtLines = append(srtLines, strconv.Itoa(i+1))
		srtLines = append(srtLines, fmt.Sprintf("%s --> %s", timeline.FormatSRTTimestamp(item.StartMS), timeline.FormatSRTTimestamp(item.EndMS)))
		srtLines = append(srtLines, text)
		srtLines = append(srtLines, "")
	}

	outName := "timeline.srt"
	if !overwriteTimeline {
		outName = "timeline.ocr.srt"
	}
	outPath := filepath.Join(outputDir, outName)
	if err := os.WriteFile(outPath, []byte(strings.Join(srtLines, "\n")), 0o644); err != nil {
		return "", fmt.Errorf("write OCR timeline %s: %w", outPath, err)
	}
	return outPath, nil
}
