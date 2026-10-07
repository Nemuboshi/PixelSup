package pipeline

import (
	"errors"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"pixelsup-go/internal/model"
	"pixelsup-go/internal/parser/idxsub"
	"pixelsup-go/internal/parser/sup"
)

func loadRenderedCues(input string, info os.FileInfo) ([]model.RenderedCue, string, error) {
	if info.IsDir() {
		cues, err := loadCuesFromImageDir(input)
		if err != nil {
			return nil, "", err
		}
		return cues, "image_dir", nil
	}

	ext := strings.ToLower(filepath.Ext(input))
	raw, err := os.ReadFile(input)
	if err != nil {
		return nil, "", fmt.Errorf("read input file: %w", err)
	}

	switch ext {
	case ".sup":
		rendered, err := sup.ParseCuesAndFrames(raw)
		if err != nil {
			return nil, "", fmt.Errorf("parse SUP payload: %w", err)
		}
		return rendered, "sup", nil
	case ".idx":
		subPath := strings.TrimSuffix(input, filepath.Ext(input)) + ".sub"
		subData, err := os.ReadFile(subPath)
		if err != nil {
			return nil, "", fmt.Errorf("read matching .sub file: %w", err)
		}
		rendered, err := idxsub.ParseCuesAndFrames(string(raw), subData)
		if err != nil {
			return nil, "", fmt.Errorf("parse IDX/SUB payload: %w", err)
		}
		return rendered, "idx", nil
	default:
		return nil, "", errors.New("input file must be .sup or .idx, or provide a directory of numbered images")
	}
}

func loadCuesFromImageDir(inputDir string) ([]model.RenderedCue, error) {
	entries, err := os.ReadDir(inputDir)
	if err != nil {
		return nil, err
	}
	type pair struct {
		n int
		p string
	}
	numbered := make([]pair, 0)
	suffix := ""
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if ext != ".png" && ext != ".jpg" && ext != ".jpeg" {
			continue
		}
		if suffix == "" {
			suffix = ext
		} else if suffix != ext {
			return nil, errors.New("image directory must use a single format only (all .png or all .jpg)")
		}
		stem := strings.TrimSuffix(entry.Name(), filepath.Ext(entry.Name()))
		n, err := strconv.Atoi(stem)
		if err != nil {
			return nil, fmt.Errorf("image filename must be numeric: %s", entry.Name())
		}
		numbered = append(numbered, pair{n: n, p: filepath.Join(inputDir, entry.Name())})
	}
	if len(numbered) == 0 {
		return nil, errors.New("no .png/.jpg files found in directory")
	}
	sort.Slice(numbered, func(i, j int) bool { return numbered[i].n < numbered[j].n })
	for i := 1; i < len(numbered); i++ {
		if numbered[i].n == numbered[i-1].n {
			return nil, fmt.Errorf("duplicate image number: %d", numbered[i].n)
		}
	}
	expected := numbered[0].n
	for _, item := range numbered {
		if item.n != expected {
			return nil, fmt.Errorf("image numbers must be consecutive; missing number: %d", expected)
		}
		expected++
	}

	cues := make([]model.RenderedCue, 0, len(numbered))
	for i, item := range numbered {
		file, err := os.Open(item.p)
		if err != nil {
			return nil, err
		}
		img, _, err := image.Decode(file)
		_ = file.Close()
		if err != nil {
			return nil, fmt.Errorf("decode image %s: %w", item.p, err)
		}
		cues = append(cues, model.RenderedCue{
			Cue: model.SubtitleCue{
				Index:   item.n,
				StartMS: i * 1000,
				EndMS:   (i + 1) * 1000,
			},
			Frame: toRGBA(img),
		})
	}
	return cues, nil
}
