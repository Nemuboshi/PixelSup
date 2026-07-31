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

const (
	ProviderOpenAILLM = "openai_llm"
	ProviderPaddleOCR = "paddle_ocr"
)

type OCRConfig struct {
	Provider       string
	MaxConcurrency int
	OpenAILLM      OpenAILLMConfig
	PaddleOCR      PaddleOCRConfig
}

type OpenAILLMConfig struct {
	APIBase             string
	APIKey              string
	Model               string
	MaxTokens           int
	TimeoutSeconds      int
	MaxRetries          int
	RetryBackoffSeconds float64
	PromptTemplate      string
}

type PaddleOCRConfig struct {
	APIURL string
	Token  string
}

// ProgressFunc reports per-sheet OCR progress while RunOCROnOutput iterates sheets.
type ProgressFunc func(done, total int, sheetName string)

const defaultTimeoutSeconds = 120
const defaultMaxRetries = 2
const defaultRetryBackoffSeconds = 1.0
const defaultOpenAIMaxTokens = 8192
const defaultOCRMaxConcurrency = 4
const strictSplitMaxAttempts = 5
const paddleTimeoutSeconds = 120
const paddleMaxRetries = 2
const paddleRetryBackoffSeconds = 1.0
const paddleUseDocOrientationClassify = false
const paddleUseDocUnwarping = false
const paddleUseTextlineOrientation = false
const maxOCRResponseBytes = 8 << 20
const maxOCRResponseBodyBytes = maxOCRResponseBytes

const DefaultJADigitsOCRPrompt = "OCR only. Output exactly {expected_count} dialogue lines in top-to-bottom order. Insert a standalone line \"0123456789\" only between adjacent dialogue lines. Do not output a leading or trailing separator. Do not include JSON or explanation."

var (
	digitsSeparatorPattern = regexp.MustCompile(`\s*0123456789\s*`)
)

var callOpenAIDigitsText = ocrOpenAIDigitsText
var callOpenAIDigitsTextWithRetry = ocrOpenAIDigitsTextWithRetry
var callPaddleSheetTextWithRetry = paddleSheetTextWithRetry
var sleepFn = func(d time.Duration) { time.Sleep(d) }
var sharedTransport = &http.Transport{
	Proxy:             http.ProxyFromEnvironment,
	MaxIdleConns:      100,
	IdleConnTimeout:   90 * time.Second,
	ForceAttemptHTTP2: true,
}
var httpClientCache sync.Map // map[int]*http.Client keyed by timeout seconds

type ocrHTTPStatusError struct {
	StatusCode int
	URL        string
	Detail     string
}

func (e *ocrHTTPStatusError) Error() string {
	return fmt.Sprintf("OCR API request failed (%d) at %s: %s", e.StatusCode, e.URL, e.Detail)
}

type ocrSchemaError struct {
	Message string
}

func (e *ocrSchemaError) Error() string {
	return e.Message
}

type ocrStrictSplitMismatchError struct {
	SheetBaseName string
	Expected      int
	Got           int
	Attempts      int
	LastLines     []string
}

func (e *ocrStrictSplitMismatchError) Error() string {
	return fmt.Sprintf(
		"strict OCR split count mismatch for %s: expected=%d got=%d after %d attempts",
		e.SheetBaseName,
		e.Expected,
		e.Got,
		e.Attempts,
	)
}

// LoadOCRConfig reads OCR settings from a YAML file.
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
		MaxConcurrency: defaultOCRMaxConcurrency,
		OpenAILLM: OpenAILLMConfig{
			TimeoutSeconds:      defaultTimeoutSeconds,
			MaxRetries:          defaultMaxRetries,
			RetryBackoffSeconds: defaultRetryBackoffSeconds,
			MaxTokens:           defaultOpenAIMaxTokens,
		},
		PaddleOCR: PaddleOCRConfig{},
	}

	var doc struct {
		OCR struct {
			Provider       string `yaml:"provider"`
			MaxConcurrency *int   `yaml:"max_concurrency"`
		} `yaml:"ocr"`
		OpenAILLM struct {
			APIBase             string   `yaml:"api_base"`
			APIKey              string   `yaml:"api_key"`
			Model               string   `yaml:"model"`
			MaxTokens           *int     `yaml:"max_tokens"`
			TimeoutSeconds      *int     `yaml:"timeout_seconds"`
			MaxRetries          *int     `yaml:"max_retries"`
			RetryBackoffSeconds *float64 `yaml:"retry_backoff_seconds"`
			PromptTemplate      *string  `yaml:"prompt_template"`
		} `yaml:"openai_llm"`
		PaddleOCR struct {
			APIURL string `yaml:"api_url"`
			Token  string `yaml:"token"`
		} `yaml:"paddle_ocr"`
	}
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		return OCRConfig{}, fmt.Errorf("parse YAML config %s: %w", path, err)
	}

	cfg.Provider = strings.TrimSpace(doc.OCR.Provider)
	if doc.OCR.MaxConcurrency != nil {
		cfg.MaxConcurrency = *doc.OCR.MaxConcurrency
	}

	cfg.OpenAILLM.APIBase = strings.TrimRight(strings.TrimSpace(doc.OpenAILLM.APIBase), "/")
	cfg.OpenAILLM.APIKey = strings.TrimSpace(doc.OpenAILLM.APIKey)
	cfg.OpenAILLM.Model = strings.TrimSpace(doc.OpenAILLM.Model)
	if doc.OpenAILLM.TimeoutSeconds != nil {
		cfg.OpenAILLM.TimeoutSeconds = *doc.OpenAILLM.TimeoutSeconds
	}
	if doc.OpenAILLM.MaxTokens != nil {
		cfg.OpenAILLM.MaxTokens = *doc.OpenAILLM.MaxTokens
	}
	if doc.OpenAILLM.MaxRetries != nil {
		cfg.OpenAILLM.MaxRetries = *doc.OpenAILLM.MaxRetries
	}
	if doc.OpenAILLM.RetryBackoffSeconds != nil {
		cfg.OpenAILLM.RetryBackoffSeconds = *doc.OpenAILLM.RetryBackoffSeconds
	}
	if doc.OpenAILLM.PromptTemplate != nil {
		cfg.OpenAILLM.PromptTemplate = *doc.OpenAILLM.PromptTemplate
	}

	cfg.PaddleOCR.APIURL = strings.TrimRight(strings.TrimSpace(doc.PaddleOCR.APIURL), "/")
	cfg.PaddleOCR.Token = strings.TrimSpace(doc.PaddleOCR.Token)

	if cfg.MaxConcurrency <= 0 {
		return OCRConfig{}, errors.New("ocr.max_concurrency must be > 0")
	}
	if cfg.Provider != ProviderOpenAILLM && cfg.Provider != ProviderPaddleOCR {
		return OCRConfig{}, errors.New("ocr.provider must be one of: openai_llm, paddle_ocr")
	}
	if cfg.Provider == ProviderOpenAILLM {
		if cfg.OpenAILLM.APIBase == "" || cfg.OpenAILLM.APIKey == "" || cfg.OpenAILLM.Model == "" {
			return OCRConfig{}, errors.New("ocr_config.yaml missing required fields for openai_llm: openai_llm.api_base, openai_llm.api_key, openai_llm.model")
		}
		if cfg.OpenAILLM.MaxRetries < 0 {
			return OCRConfig{}, errors.New("openai_llm.max_retries must be >= 0")
		}
		if cfg.OpenAILLM.RetryBackoffSeconds < 0 {
			return OCRConfig{}, errors.New("openai_llm.retry_backoff_seconds must be >= 0")
		}
		if cfg.OpenAILLM.TimeoutSeconds <= 0 {
			return OCRConfig{}, errors.New("openai_llm.timeout_seconds must be > 0")
		}
		if doc.OpenAILLM.MaxTokens != nil && cfg.OpenAILLM.MaxTokens <= 0 {
			return OCRConfig{}, errors.New("openai_llm.max_tokens must be > 0")
		}
	}
	if cfg.Provider == ProviderPaddleOCR {
		if cfg.PaddleOCR.APIURL == "" || cfg.PaddleOCR.Token == "" {
			return OCRConfig{}, errors.New("ocr_config.yaml missing required fields for paddle_ocr: paddle_ocr.api_url, paddle_ocr.token")
		}
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

func normalizeOCRContentText(contentText string) string {
	text := strings.TrimSpace(contentText)
	if strings.HasPrefix(text, "```") {
		text = strings.Trim(text, "`")
		text = strings.TrimSpace(text)
		if strings.HasPrefix(text, "json") {
			text = strings.TrimSpace(text[4:])
		}
	}
	return text
}

func parseTextFromResponse(contentText string) string {
	text := normalizeOCRContentText(contentText)
	var envelope struct {
		Text  string   `json:"text"`
		Lines []string `json:"lines"`
	}
	if err := json.Unmarshal([]byte(text), &envelope); err == nil {
		if strings.TrimSpace(envelope.Text) != "" {
			return strings.TrimSpace(envelope.Text)
		}
		if len(envelope.Lines) > 0 {
			return strings.TrimSpace(strings.Join(envelope.Lines, " 0123456789 "))
		}
		// Treat valid-but-empty JSON object payloads like {} as empty OCR output.
		return ""
	}
	return strings.TrimSpace(text)
}

func splitLinesByDigitSeparator(contentText string, expectedCount int) []string {
	return alignLineCount(splitLinesByDigitSeparatorRaw(contentText), expectedCount)
}

func splitLinesByDigitSeparatorRaw(contentText string) []string {
	text := strings.TrimSpace(contentText)
	if text == "" {
		return nil
	}
	parts := digitsSeparatorPattern.Split(text, -1)
	lines := make([]string, 0, len(parts))
	for _, p := range parts {
		s := strings.TrimSpace(p)
		if s == "" {
			continue
		}
		lines = append(lines, s)
	}
	return lines
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

func buildPrompt(config OpenAILLMConfig, expectedCount int) (string, error) {
	template := strings.TrimSpace(config.PromptTemplate)
	if template == "" {
		template = DefaultJADigitsOCRPrompt
	}
	return renderTemplate(template, expectedCount)
}

func ocrOpenAIDigitsText(config OpenAILLMConfig, imagePath string, expectedCount int) (string, error) {
	contentText, err := openAIChatCompletion(config, imagePath, expectedCount)
	if err != nil {
		return "", err
	}
	return parseTextFromResponse(contentText), nil
}

func openAIChatCompletion(config OpenAILLMConfig, imagePath string, expectedCount int) (string, error) {
	prompt, err := buildPrompt(config, expectedCount)
	if err != nil {
		return "", err
	}
	uri, err := dataURI(imagePath)
	if err != nil {
		return "", err
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
	}
	if config.MaxTokens > 0 {
		payload["max_tokens"] = config.MaxTokens
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal OCR request payload: %w", err)
	}
	url := config.APIBase + "/chat/completions"
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(config.TimeoutSeconds)*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("build OCR request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+config.APIKey)
	req.Header.Set("Content-Type", "application/json")
	resp, err := getHTTPClient(config.TimeoutSeconds).Do(req)
	if err != nil {
		return "", fmt.Errorf("OCR API request failed at %s: %w", url, err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxOCRResponseBytes+1))
	if err != nil {
		return "", fmt.Errorf("read OCR API response: %w", err)
	}
	if len(respBody) > maxOCRResponseBytes {
		return "", &ocrSchemaError{Message: fmt.Sprintf("OCR API response body too large (> %d bytes)", maxOCRResponseBytes)}
	}
	if resp.StatusCode >= 400 {
		detail := strings.TrimSpace(string(respBody))
		if len(detail) > 500 {
			detail = detail[:500]
		}
		return "", &ocrHTTPStatusError{StatusCode: resp.StatusCode, URL: url, Detail: detail}
	}
	var data struct {
		Choices []struct {
			Message struct {
				Content any `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &data); err != nil {
		return "", &ocrSchemaError{Message: fmt.Sprintf("decode OCR response JSON: %v", err)}
	}
	if len(data.Choices) == 0 {
		return "", &ocrSchemaError{Message: "decode OCR response JSON: choices is empty"}
	}
	return extractTextContent(data.Choices[0].Message.Content), nil
}

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

func ocrOpenAIDigitsTextWithRetry(config OpenAILLMConfig, imagePath string, expectedCount int) (string, error) {
	attempts := config.MaxRetries + 1
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		text, err := callOpenAIDigitsText(config, imagePath, expectedCount)
		if err == nil {
			return text, nil
		}
		lastErr = err
		if !shouldRetryOCRError(err) || attempt >= attempts-1 {
			break
		}
		if config.RetryBackoffSeconds > 0 {
			backoffSeconds := config.RetryBackoffSeconds * math.Pow(2, float64(attempt))
			sleepFn(time.Duration(backoffSeconds * float64(time.Second)))
		}
	}
	return "", fmt.Errorf("OCR failed for sheet %s after %d attempts: %w", filepath.Base(imagePath), attempts, lastErr)
}

func ocrStrictLines(
	provider string,
	openAIConfig OpenAILLMConfig,
	paddleConfig PaddleOCRConfig,
	imagePath string,
	expectedCount int,
) ([]string, error) {
	var lastLines []string
	for attempt := 1; attempt <= strictSplitMaxAttempts; attempt++ {
		var (
			text string
			err  error
		)
		switch provider {
		case ProviderOpenAILLM:
			text, err = callOpenAIDigitsTextWithRetry(openAIConfig, imagePath, expectedCount)
		case ProviderPaddleOCR:
			text, err = callPaddleSheetTextWithRetry(paddleConfig, imagePath, expectedCount)
		default:
			return nil, fmt.Errorf("unsupported ocr provider: %s", provider)
		}
		if err != nil {
			return nil, err
		}
		lines := splitLinesByDigitSeparatorRaw(text)
		lastLines = lines
		if len(lines) == expectedCount {
			return lines, nil
		}
	}
	return nil, fmt.Errorf(
		"%w",
		&ocrStrictSplitMismatchError{
			SheetBaseName: filepath.Base(imagePath),
			Expected:      expectedCount,
			Got:           len(lastLines),
			Attempts:      strictSplitMaxAttempts,
			LastLines:     append([]string(nil), lastLines...),
		},
	)
}

type paddleLayoutResponse struct {
	Result struct {
		OCRResults []struct {
			PrunedResult struct {
				RecTexts []string      `json:"rec_texts"`
				RecBoxes [][]float64   `json:"rec_boxes"`
				DTPolys  [][][]float64 `json:"dt_polys"`
			} `json:"prunedResult"`
		} `json:"ocrResults"`
	} `json:"result"`
}

type paddleTextBox struct {
	Text   string
	Left   float64
	Top    float64
	Right  float64
	Bottom float64
}

func paddleSheetTextWithRetry(config PaddleOCRConfig, imagePath string, expectedCount int) (string, error) {
	attempts := paddleMaxRetries + 1
	var lastErr error
	for attempt := 0; attempt < attempts; attempt++ {
		text, err := paddleSheetText(config, imagePath, expectedCount)
		if err == nil {
			return text, nil
		}
		lastErr = err
		if !shouldRetryOCRError(err) || attempt >= attempts-1 {
			break
		}
		if paddleRetryBackoffSeconds > 0 {
			backoffSeconds := paddleRetryBackoffSeconds * math.Pow(2, float64(attempt))
			sleepFn(time.Duration(backoffSeconds * float64(time.Second)))
		}
	}
	return "", fmt.Errorf("OCR failed for sheet %s after %d attempts: %w", filepath.Base(imagePath), attempts, lastErr)
}

func paddleSheetText(config PaddleOCRConfig, imagePath string, _ int) (string, error) {
	raw, err := os.ReadFile(imagePath)
	if err != nil {
		return "", fmt.Errorf("read sheet image %s: %w", imagePath, err)
	}
	payload := map[string]any{
		"file":                      base64.StdEncoding.EncodeToString(raw),
		"fileType":                  1,
		"useDocOrientationClassify": paddleUseDocOrientationClassify,
		"useDocUnwarping":           paddleUseDocUnwarping,
		"useTextlineOrientation":    paddleUseTextlineOrientation,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal paddle OCR request payload: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(paddleTimeoutSeconds)*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, config.APIURL, bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("build paddle OCR request: %w", err)
	}
	req.Header.Set("Authorization", "token "+config.Token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := getHTTPClient(paddleTimeoutSeconds).Do(req)
	if err != nil {
		return "", fmt.Errorf("paddle OCR request failed at %s: %w", config.APIURL, err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, maxOCRResponseBytes+1))
	if err != nil {
		return "", fmt.Errorf("read paddle OCR response: %w", err)
	}
	if len(respBody) > maxOCRResponseBytes {
		return "", &ocrSchemaError{Message: fmt.Sprintf("paddle OCR response body too large (> %d bytes)", maxOCRResponseBytes)}
	}
	if resp.StatusCode >= 400 {
		detail := strings.TrimSpace(string(respBody))
		if len(detail) > 500 {
			detail = detail[:500]
		}
		return "", &ocrHTTPStatusError{StatusCode: resp.StatusCode, URL: config.APIURL, Detail: detail}
	}
	return extractTextFromPaddleResponse(respBody)
}

func extractTextFromPaddleResponse(raw []byte) (string, error) {
	var payload paddleLayoutResponse
	if err := json.Unmarshal(raw, &payload); err != nil {
		return "", fmt.Errorf("decode paddle OCR response: %w", err)
	}
	if len(payload.Result.OCRResults) == 0 {
		return "", nil
	}
	parts := make([]string, 0, len(payload.Result.OCRResults))
	for _, page := range payload.Result.OCRResults {
		boxes, err := extractPaddleTextBoxes(page.PrunedResult)
		if err != nil {
			return "", err
		}
		text := strings.TrimSpace(rebuildPaddleText(boxes))
		if text == "" {
			continue
		}
		parts = append(parts, text)
	}
	return strings.Join(parts, "\n\n"), nil
}

func extractPaddleTextBoxes(pruned struct {
	RecTexts []string      `json:"rec_texts"`
	RecBoxes [][]float64   `json:"rec_boxes"`
	DTPolys  [][][]float64 `json:"dt_polys"`
}) ([]paddleTextBox, error) {
	if len(pruned.RecTexts) == 0 {
		return nil, nil
	}
	if len(pruned.RecBoxes) == 0 && len(pruned.DTPolys) == 0 {
		return nil, errors.New("decode paddle OCR response: missing prunedResult.rec_boxes")
	}
	boxes := make([]paddleTextBox, 0, len(pruned.RecTexts))
	for i, text := range pruned.RecTexts {
		text = strings.TrimSpace(text)
		if text == "" {
			continue
		}
		left, top, right, bottom, ok := extractPaddleBounds(pruned, i)
		if !ok {
			return nil, fmt.Errorf("decode paddle OCR response: missing geometry for prunedResult.rec_texts[%d]", i)
		}
		boxes = append(boxes, paddleTextBox{
			Text:   text,
			Left:   left,
			Top:    top,
			Right:  right,
			Bottom: bottom,
		})
	}
	return boxes, nil
}

func extractPaddleBounds(pruned struct {
	RecTexts []string      `json:"rec_texts"`
	RecBoxes [][]float64   `json:"rec_boxes"`
	DTPolys  [][][]float64 `json:"dt_polys"`
}, idx int) (float64, float64, float64, float64, bool) {
	if idx < len(pruned.RecBoxes) && len(pruned.RecBoxes[idx]) >= 4 {
		box := pruned.RecBoxes[idx]
		return box[0], box[1], box[2], box[3], true
	}
	if idx < len(pruned.DTPolys) && len(pruned.DTPolys[idx]) > 0 {
		left := pruned.DTPolys[idx][0][0]
		right := left
		top := pruned.DTPolys[idx][0][1]
		bottom := top
		for _, point := range pruned.DTPolys[idx] {
			if len(point) < 2 {
				continue
			}
			if point[0] < left {
				left = point[0]
			}
			if point[0] > right {
				right = point[0]
			}
			if point[1] < top {
				top = point[1]
			}
			if point[1] > bottom {
				bottom = point[1]
			}
		}
		return left, top, right, bottom, true
	}
	return 0, 0, 0, 0, false
}

func rebuildPaddleText(boxes []paddleTextBox) string {
	if len(boxes) == 0 {
		return ""
	}
	sort.Slice(boxes, func(i, j int) bool {
		if math.Abs(boxes[i].Top-boxes[j].Top) > 1 {
			return boxes[i].Top < boxes[j].Top
		}
		return boxes[i].Left < boxes[j].Left
	})
	avgHeight := 0.0
	for _, box := range boxes {
		avgHeight += maxFloat(1, box.Bottom-box.Top)
	}
	avgHeight /= float64(len(boxes))
	lineThreshold := maxFloat(4, avgHeight*0.5)

	lines := make([][]paddleTextBox, 0)
	for _, box := range boxes {
		if len(lines) == 0 {
			lines = append(lines, []paddleTextBox{box})
			continue
		}
		lastLine := lines[len(lines)-1]
		lineTop, lineBottom := lineVerticalSpan(lastLine)
		lineCenter := (lineTop + lineBottom) / 2
		boxCenter := (box.Top + box.Bottom) / 2
		if math.Abs(boxCenter-lineCenter) <= lineThreshold {
			lines[len(lines)-1] = append(lines[len(lines)-1], box)
			continue
		}
		lines = append(lines, []paddleTextBox{box})
	}

	rendered := make([]string, 0, len(lines))
	for _, line := range lines {
		sort.Slice(line, func(i, j int) bool { return line[i].Left < line[j].Left })
		var b strings.Builder
		for i, box := range line {
			if i > 0 {
				prev := line[i-1]
				gap := box.Left - prev.Right
				if gap > maxFloat(6, (prev.Right-prev.Left)*0.25) {
					b.WriteByte(' ')
				}
			}
			b.WriteString(strings.TrimSpace(box.Text))
		}
		rendered = append(rendered, strings.TrimSpace(b.String()))
	}
	return strings.Join(rendered, "\n\n")
}

func lineVerticalSpan(line []paddleTextBox) (float64, float64) {
	top := line[0].Top
	bottom := line[0].Bottom
	for _, box := range line[1:] {
		if box.Top < top {
			top = box.Top
		}
		if box.Bottom > bottom {
			bottom = box.Bottom
		}
	}
	return top, bottom
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
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

func RunOCROnOutput(outputDir, configPath string, strict bool, progressCB ProgressFunc) (string, error) {
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
	if progressCB != nil {
		progressCB(0, len(sheetNames), "")
	}

	type ocrJob struct {
		sheetName string
	}
	type ocrResult struct {
		sheetName string
		lines     []string
		err       error
		strictErr *ocrStrictSplitMismatchError
	}
	cueTexts := make(map[int]string, len(payload.Items))
	jobs := make(chan ocrJob)
	results := make(chan ocrResult, len(sheetNames))
	workerCount := config.MaxConcurrency
	if workerCount > len(sheetNames) {
		workerCount = len(sheetNames)
	}
	var workers sync.WaitGroup
	for i := 0; i < workerCount; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for job := range jobs {
				sheetName := job.sheetName
				sheetPath := filepath.Join(outputDir, sheetName)
				if _, err := os.Stat(sheetPath); err != nil {
					if errors.Is(err, os.ErrNotExist) {
						results <- ocrResult{sheetName: sheetName, err: fmt.Errorf("sheet image missing: %s", sheetPath)}
						continue
					}
					results <- ocrResult{sheetName: sheetName, err: fmt.Errorf("stat sheet image %s: %w", sheetPath, err)}
					continue
				}
				sheetItems := bySheet[sheetName]
				expectedCount := len(sheetItems)

				if strict {
					lines, strictErr := ocrStrictLines(config.Provider, config.OpenAILLM, config.PaddleOCR, sheetPath, expectedCount)
					if strictErr != nil {
						var mismatchErr *ocrStrictSplitMismatchError
						if errors.As(strictErr, &mismatchErr) {
							results <- ocrResult{
								sheetName: sheetName,
								lines:     alignLineCount(mismatchErr.LastLines, expectedCount),
								strictErr: mismatchErr,
							}
							continue
						}
						results <- ocrResult{sheetName: sheetName, err: strictErr}
						continue
					}
					results <- ocrResult{sheetName: sheetName, lines: lines}
					continue
				}
				var text string
				var textErr error
				switch config.Provider {
				case ProviderOpenAILLM:
					text, textErr = callOpenAIDigitsTextWithRetry(config.OpenAILLM, sheetPath, expectedCount)
				case ProviderPaddleOCR:
					text, textErr = callPaddleSheetTextWithRetry(config.PaddleOCR, sheetPath, expectedCount)
				default:
					textErr = fmt.Errorf("unsupported ocr provider: %s", config.Provider)
				}
				if textErr != nil {
					results <- ocrResult{sheetName: sheetName, err: textErr}
					continue
				}
				results <- ocrResult{
					sheetName: sheetName,
					lines:     splitLinesByDigitSeparator(text, expectedCount),
				}
			}
		}()
	}

	go func() {
		for _, sheetName := range sheetNames {
			jobs <- ocrJob{sheetName: sheetName}
		}
		close(jobs)
		workers.Wait()
		close(results)
	}()

	strictMismatchErrors := make([]*ocrStrictSplitMismatchError, 0)
	for completed := 0; completed < len(sheetNames); completed++ {
		result, ok := <-results
		if !ok {
			return "", errors.New("OCR worker pool closed unexpectedly")
		}
		if result.err != nil {
			return "", result.err
		}
		for i, item := range bySheet[result.sheetName] {
			if i >= len(result.lines) {
				break
			}
			cueTexts[item.CueIndex] = strings.TrimSpace(result.lines[i])
		}
		if result.strictErr != nil {
			strictMismatchErrors = append(strictMismatchErrors, result.strictErr)
		}
		if progressCB != nil {
			progressCB(completed+1, len(sheetNames), result.sheetName)
		}
	}

	items := append([]mappingItem{}, payload.Items...)
	sort.Slice(items, func(i, j int) bool { return items[i].CueIndex < items[j].CueIndex })
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
	outPath := filepath.Join(outputDir, "timeline.srt")
	if err := os.WriteFile(outPath, []byte(strings.Join(srtLines, "\n")), 0o644); err != nil {
		return "", fmt.Errorf("write OCR timeline %s: %w", outPath, err)
	}
	if len(strictMismatchErrors) > 0 {
		parts := make([]string, 0, len(strictMismatchErrors))
		for _, e := range strictMismatchErrors {
			parts = append(parts, "- "+e.Error())
		}
		return outPath, fmt.Errorf("strict OCR mismatches:\n%s", strings.Join(parts, "\n"))
	}
	return outPath, nil
}
