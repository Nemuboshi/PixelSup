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
	"strconv"
	"strings"
	"time"
)

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
