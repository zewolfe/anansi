package output

import (
	"encoding/csv"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"

	"github.com/zewolfe/anansi/internal/config"
)

// CSVWriter writes TrialResult rows to per-config CSV files.
type CSVWriter struct {
	mu      sync.Mutex
	baseDir string
	files   map[string]*csvFile
}

type csvFile struct {
	file   *os.File
	writer *csv.Writer
}

var csvHeader = []string{
	"config_hash", "runtime", "format", "model", "scenario", "rep",
	"t0_ns", "t1_ns", "t2_ns", "t3_ns", "t4_ns", "t5_ns", "t6_ns", "t7_ns",
	"ttft_ms", "t_orch_ms", "t_serve_ms", "t_load_ms", "t_init_ms",
	"gpu_mem_mb", "error",
}

func NewCSVWriter(baseDir string) *CSVWriter {
	return &CSVWriter{
		baseDir: filepath.Join(baseDir, "raw"),
		files:   make(map[string]*csvFile),
	}
}

// WriteResult appends a single trial result to the appropriate CSV file.
func (w *CSVWriter) WriteResult(result *config.TrialResult) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	cf, err := w.getOrCreateFile(result.ConfigHash)
	if err != nil {
		return err
	}

	row := []string{
		result.ConfigHash,
		result.Runtime,
		result.Format,
		result.Model,
		result.Scenario,
		strconv.Itoa(result.Rep),
		i64(result.T0),
		i64(result.T1),
		i64(result.T2),
		i64(result.T3),
		i64(result.T4),
		i64(result.T5),
		i64(result.T6),
		i64(result.T7),
		f64(result.TTFT_ms),
		f64(result.TOrch_ms),
		f64(result.TServe_ms),
		f64(result.TLoad_ms),
		f64(result.TInit_ms),
		strconv.Itoa(result.GPUMemMB),
		result.Error,
	}

	if err := cf.writer.Write(row); err != nil {
		return fmt.Errorf("writing CSV row: %w", err)
	}
	cf.writer.Flush()
	return cf.writer.Error()
}

// Close flushes and closes all open CSV files.
func (w *CSVWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	var firstErr error
	for _, cf := range w.files {
		cf.writer.Flush()
		if err := cf.file.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	w.files = make(map[string]*csvFile)
	return firstErr
}

func (w *CSVWriter) getOrCreateFile(hash string) (*csvFile, error) {
	if cf, ok := w.files[hash]; ok {
		return cf, nil
	}

	path := filepath.Join(w.baseDir, hash+".csv")

	_, statErr := os.Stat(path)
	needHeader := os.IsNotExist(statErr)

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("creating CSV file %s: %w", path, err)
	}

	cw := csv.NewWriter(f)
	if needHeader {
		if err := cw.Write(csvHeader); err != nil {
			f.Close()
			return nil, fmt.Errorf("writing CSV header: %w", err)
		}
		cw.Flush()
	}

	cf := &csvFile{file: f, writer: cw}
	w.files[hash] = cf
	return cf, nil
}

func i64(v int64) string {
	if v == 0 {
		return ""
	}
	return strconv.FormatInt(v, 10)
}

func f64(v float64) string {
	if v == -1 {
		return ""
	}
	return strconv.FormatFloat(v, 'f', 3, 64)
}
