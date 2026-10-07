package ocr

import (
	"errors"
	"fmt"
	"gopkg.in/yaml.v3"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
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
	APIURL           string
	Token            string
	Model            string
	ResponseDumpPath string
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
const defaultPaddleJobURL = "https://paddleocr.aistudio-app.com/api/v2/ocr/jobs"
const defaultPaddleModel = "PP-OCRv6"
const paddlePollInterval = 5 * time.Second
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
			Model  string `yaml:"model"`
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
	if cfg.PaddleOCR.APIURL == "" {
		cfg.PaddleOCR.APIURL = defaultPaddleJobURL
	}
	cfg.PaddleOCR.Token = strings.TrimSpace(doc.PaddleOCR.Token)
	cfg.PaddleOCR.Model = strings.TrimSpace(doc.PaddleOCR.Model)
	if cfg.PaddleOCR.Model == "" {
		cfg.PaddleOCR.Model = defaultPaddleModel
	}

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
