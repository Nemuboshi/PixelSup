package ocr

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"pixelsup-go/internal/timeline"
	"sort"
	"strconv"
	"strings"
	"sync"
)

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
	return RunOCROnOutputWithDump(outputDir, configPath, strict, "", progressCB)
}

func RunOCROnOutputWithDump(outputDir, configPath string, strict bool, responseDumpDir string, progressCB ProgressFunc) (string, error) {
	config, err := LoadOCRConfig(configPath)
	if err != nil {
		return "", err
	}
	if responseDumpDir != "" {
		if config.Provider != ProviderPaddleOCR {
			return "", errors.New("--dump-paddle-responses requires ocr.provider: paddle_ocr")
		}
		if err := os.MkdirAll(responseDumpDir, 0o755); err != nil {
			return "", fmt.Errorf("create PaddleOCR response dump directory: %w", err)
		}
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
				paddleConfig := config.PaddleOCR
				if responseDumpDir != "" {
					base := strings.TrimSuffix(filepath.Base(sheetName), filepath.Ext(sheetName))
					paddleConfig.ResponseDumpPath = filepath.Join(responseDumpDir, base+".jsonl")
					if err := os.WriteFile(paddleConfig.ResponseDumpPath, nil, 0o644); err != nil {
						results <- ocrResult{sheetName: sheetName, err: fmt.Errorf("create PaddleOCR response dump: %w", err)}
						continue
					}
				}

				if strict {
					lines, strictErr := ocrStrictLines(config.Provider, config.OpenAILLM, paddleConfig, sheetPath, expectedCount)
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
					text, textErr = callPaddleSheetTextWithRetry(paddleConfig, sheetPath, expectedCount)
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
