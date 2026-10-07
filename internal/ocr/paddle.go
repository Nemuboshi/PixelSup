package ocr

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

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
	file, err := os.Open(imagePath)
	if err != nil {
		return "", fmt.Errorf("open sheet image %s: %w", imagePath, err)
	}
	defer file.Close()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("model", config.Model); err != nil {
		return "", err
	}
	optional, _ := json.Marshal(map[string]bool{
		"useDocOrientationClassify": false,
		"useDocUnwarping":           false,
		"useTextlineOrientation":    false,
	})
	if err := writer.WriteField("optionalPayload", string(optional)); err != nil {
		return "", err
	}
	part, err := writer.CreateFormFile("file", filepath.Base(imagePath))
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(part, file); err != nil {
		return "", fmt.Errorf("write paddle OCR upload: %w", err)
	}
	if err := writer.Close(); err != nil {
		return "", err
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(paddleTimeoutSeconds)*time.Second)
	defer cancel()
	client := getHTTPClient(paddleTimeoutSeconds)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, config.APIURL, &body)
	if err != nil {
		return "", fmt.Errorf("build paddle OCR request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+config.Token)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("submit paddle OCR job at %s: %w", config.APIURL, err)
	}
	var submitted struct {
		Data struct {
			JobID string `json:"jobId"`
		} `json:"data"`
	}
	if err := decodePaddleResponse(resp, &submitted, config.APIURL, config.ResponseDumpPath, "submit"); err != nil {
		return "", err
	}
	if submitted.Data.JobID == "" {
		return "", &ocrSchemaError{Message: "decode paddle OCR response: missing data.jobId"}
	}

	for {
		pollURL := config.APIURL + "/" + url.PathEscape(submitted.Data.JobID)
		pollReq, err := http.NewRequestWithContext(ctx, http.MethodGet, pollURL, nil)
		if err != nil {
			return "", err
		}
		pollReq.Header.Set("Authorization", "Bearer "+config.Token)
		var status struct {
			Data struct {
				State  string `json:"state"`
				Error  string `json:"errorMsg"`
				Result struct {
					JSONURL string `json:"jsonUrl"`
				} `json:"resultUrl"`
			} `json:"data"`
		}
		resp, err = client.Do(pollReq)
		if err != nil {
			return "", fmt.Errorf("poll paddle OCR job: %w", err)
		}
		if err := decodePaddleResponse(resp, &status, pollURL, config.ResponseDumpPath, "poll"); err != nil {
			return "", err
		}
		switch status.Data.State {
		case "pending", "running":
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(paddlePollInterval):
			}
		case "failed":
			return "", fmt.Errorf("paddle OCR job failed: %s", status.Data.Error)
		case "done":
			if status.Data.Result.JSONURL == "" {
				return "", &ocrSchemaError{Message: "decode paddle OCR response: missing data.resultUrl.jsonUrl"}
			}
			return downloadPaddleMarkdown(ctx, client, status.Data.Result.JSONURL, config.ResponseDumpPath)
		default:
			return "", &ocrSchemaError{Message: "decode paddle OCR response: unexpected job state " + status.Data.State}
		}
	}
}

func decodePaddleResponse(resp *http.Response, dst any, requestURL, dumpPath, stage string) error {
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxOCRResponseBytes+1))
	if err != nil {
		return fmt.Errorf("read paddle OCR response: %w", err)
	}
	if err := appendPaddleResponseJSONL(dumpPath, stage, resp.StatusCode, requestURL, raw); err != nil {
		return fmt.Errorf("write PaddleOCR response dump: %w", err)
	}
	if len(raw) > maxOCRResponseBytes {
		return &ocrSchemaError{Message: "paddle OCR response body too large"}
	}
	if resp.StatusCode >= 400 {
		detail := strings.TrimSpace(string(raw))
		if len(detail) > 500 {
			detail = detail[:500]
		}
		return &ocrHTTPStatusError{StatusCode: resp.StatusCode, URL: requestURL, Detail: detail}
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("decode paddle OCR response: %w", err)
	}
	return nil
}

type paddleResponseDumpRecord struct {
	Stage      string          `json:"stage"`
	StatusCode int             `json:"status_code"`
	URL        string          `json:"url"`
	Body       json.RawMessage `json:"body,omitempty"`
	BodyText   string          `json:"body_text,omitempty"`
}

func appendPaddleResponseJSONL(path, stage string, statusCode int, responseURL string, raw []byte) error {
	if path == "" {
		return nil
	}
	record := paddleResponseDumpRecord{Stage: stage, StatusCode: statusCode, URL: responseURL}
	if json.Valid(raw) {
		record.Body = json.RawMessage(raw)
	} else {
		record.BodyText = string(raw)
	}
	line, err := json.Marshal(record)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	_, writeErr := file.Write(line)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

func downloadPaddleMarkdown(ctx context.Context, client *http.Client, resultURL, dumpPath string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, resultURL, nil)
	if err != nil {
		return "", err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download paddle OCR result: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxOCRResponseBytes+1))
	if err != nil {
		return "", err
	}
	for _, line := range bytes.Split(raw, []byte("\n")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		if err := appendPaddleResponseJSONL(dumpPath, "result", resp.StatusCode, resultURL, line); err != nil {
			return "", fmt.Errorf("write PaddleOCR response dump: %w", err)
		}
	}
	if resp.StatusCode >= 400 {
		return "", &ocrHTTPStatusError{StatusCode: resp.StatusCode, URL: resultURL}
	}
	if len(raw) > maxOCRResponseBytes {
		return "", &ocrSchemaError{Message: "paddle OCR result too large"}
	}
	var text strings.Builder
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var row struct {
			Result struct {
				OCRResults []struct {
					PrunedResult struct {
						RecTexts []string      `json:"rec_texts"`
						RecBoxes [][]float64   `json:"rec_boxes"`
						DTPolys  [][][]float64 `json:"dt_polys"`
					} `json:"prunedResult"`
				} `json:"ocrResults"`
				Layout []struct {
					Markdown struct {
						Text string `json:"text"`
					} `json:"markdown"`
				} `json:"layoutParsingResults"`
			} `json:"result"`
		}
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			return "", fmt.Errorf("decode paddle OCR JSONL result: %w", err)
		}
		if len(row.Result.OCRResults) > 0 {
			resultJSON, err := json.Marshal(map[string]any{"result": row.Result})
			if err != nil {
				return "", err
			}
			pageText, err := extractTextFromPaddleResponse(resultJSON)
			if err != nil {
				return "", err
			}
			if text.Len() > 0 && pageText != "" {
				text.WriteString("\n")
			}
			text.WriteString(pageText)
			continue
		}
		for _, page := range row.Result.Layout {
			if text.Len() > 0 {
				text.WriteString("\n")
			}
			text.WriteString(paddleMarkdownText(page.Markdown.Text))
		}
	}
	return text.String(), nil
}

func paddleMarkdownText(markdown string) string {
	text := regexp.MustCompile(`(?i)</(?:td|th|tr|p|div|li)>`).ReplaceAllString(markdown, "\n")
	text = regexp.MustCompile(`<[^>]*>`).ReplaceAllString(text, "")
	text = html.UnescapeString(text)
	return strings.TrimSpace(text)
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
