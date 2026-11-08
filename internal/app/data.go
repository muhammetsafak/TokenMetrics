package app

// Data bindings: hand-entered records, CSV / JSON import and export.
//
// File paths never come from the frontend. The Wails layer (app.go) asks the
// user for a file with a native dialog and passes the chosen path to the
// *File methods below; the Service methods are not bindings themselves.

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/muhammetsafak/TokenMetrics/internal/dataio"
	"github.com/muhammetsafak/TokenMetrics/internal/model"
	"github.com/muhammetsafak/TokenMetrics/internal/store"
)

// maxManualRows bounds the manual record list sent to the screen.
const maxManualRows = 500

// Formats of Export and import.
const (
	FormatCSV  = "csv"
	FormatJSON = "json"
)

// ManualRecordDTO is a hand-entered usage record.
type ManualRecordDTO struct {
	// ExternalID identifies the record; empty when adding a new one.
	ExternalID         string   `json:"externalId"`
	TimestampMs        int64    `json:"timestampMs"`
	Service            string   `json:"service"`
	Model              string   `json:"model"`
	Project            string   `json:"project"`
	Session            string   `json:"session"`
	InputTokens        int64    `json:"inputTokens"`
	OutputTokens       int64    `json:"outputTokens"`
	CacheReadTokens    int64    `json:"cacheReadTokens"`
	CacheWrite5mTokens int64    `json:"cacheWrite5mTokens"`
	CacheWrite1hTokens int64    `json:"cacheWrite1hTokens"`
	ThinkingTokens     int64    `json:"thinkingTokens"`
	RequestCount       int64    `json:"requestCount"`
	CostUSD            *float64 `json:"costUsd"` // null = not reported
}

func manualDTO(r model.UsageRecord) ManualRecordDTO {
	return ManualRecordDTO{
		ExternalID: r.ExternalID, TimestampMs: r.Timestamp.UnixMilli(),
		Service: r.Service, Model: r.Model, Project: r.Project, Session: r.Session,
		InputTokens: r.InputTokens, OutputTokens: r.OutputTokens, CacheReadTokens: r.CacheReadTokens,
		CacheWrite5mTokens: r.CacheWrite5mTokens, CacheWrite1hTokens: r.CacheWrite1hTokens,
		ThinkingTokens: r.ThinkingTokens, RequestCount: r.RequestCount, CostUSD: r.ReportedCostUSD,
	}
}

func (d ManualRecordDTO) record() model.UsageRecord {
	return model.UsageRecord{
		Source: model.SourceManual, ExternalID: d.ExternalID, Timestamp: time.UnixMilli(d.TimestampMs),
		Service: d.Service, Model: d.Model, Project: d.Project, Session: d.Session,
		InputTokens: d.InputTokens, OutputTokens: d.OutputTokens, CacheReadTokens: d.CacheReadTokens,
		CacheWrite5mTokens: d.CacheWrite5mTokens, CacheWrite1hTokens: d.CacheWrite1hTokens,
		ThinkingTokens: d.ThinkingTokens, RequestCount: d.RequestCount, ReportedCostUSD: d.CostUSD,
	}
}

// ManualListDTO is the list of hand-entered records, newest first.
type ManualListDTO struct {
	Rows []ManualRecordDTO `json:"rows"`
	// Total is the number of manual records; Truncated is true when Rows
	// holds only the newest ones.
	Total     int  `json:"total"`
	Truncated bool `json:"truncated"`
}

// manualErrors translates the validation messages of internal/dataio.
var manualErrors = map[string]string{
	"timestamp is required":                              "Tarih ve saat girilmeli.",
	"service is required":                                "Servis girilmeli.",
	"model is required":                                  "Model girilmeli.",
	"timestamp is in the future":                         "Tarih ve saat gelecekte olamaz (en fazla 24 saat ilerisi kabul edilir).",
	"reported_cost_usd is negative":                      "Maliyet negatif olamaz.",
	"reported_cost_usd is not a sane number":             "Maliyet geçerli bir sayı olmalı.",
	"at least one token field must be greater than zero": "En az bir token alanı 0'dan büyük olmalı.",
}

func manualError(err error) error {
	switch {
	case errors.Is(err, dataio.ErrManualNotFound):
		return errors.New("Kayıt bulunamadı; başka bir yerde silinmiş olabilir.")
	case errors.Is(err, dataio.ErrNotManual):
		return errors.New("Yalnız elle girilen kayıtlar değiştirilebilir.")
	}
	if msg, ok := manualErrors[err.Error()]; ok {
		return errors.New(msg)
	}
	return err
}

// ManualRecords lists the hand-entered records, newest first.
func (s *Service) ManualRecords(ctx context.Context) (ManualListDTO, error) {
	var all []model.UsageRecord
	err := s.st.EachUsageRecord(ctx, store.Filter{Sources: []model.Source{model.SourceManual}}, func(r model.UsageRecord) error {
		all = append(all, r)
		return nil
	})
	if err != nil {
		return ManualListDTO{}, err
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].Timestamp.After(all[j].Timestamp) })
	out := ManualListDTO{Rows: []ManualRecordDTO{}, Total: len(all)}
	if len(all) > maxManualRows {
		all, out.Truncated = all[:maxManualRows], true
	}
	for _, r := range all {
		out.Rows = append(out.Rows, manualDTO(r))
	}
	return out, nil
}

// AddManualRecord stores a hand-entered record and returns it as stored.
func (s *Service) AddManualRecord(ctx context.Context, d ManualRecordDTO) (ManualRecordDTO, error) {
	r, err := dataio.AddManual(ctx, s.st, s.deviceID, d.record())
	if err != nil {
		return ManualRecordDTO{}, manualError(err)
	}
	s.budgetStateChanged()
	return manualDTO(r), nil
}

// UpdateManualRecord replaces the values of a hand-entered record.
func (s *Service) UpdateManualRecord(ctx context.Context, d ManualRecordDTO) (ManualRecordDTO, error) {
	r, err := dataio.UpdateManual(ctx, s.st, d.record())
	if err != nil {
		return ManualRecordDTO{}, manualError(err)
	}
	s.budgetStateChanged()
	return manualDTO(r), nil
}

// DeleteManualRecord removes a hand-entered record.
func (s *Service) DeleteManualRecord(ctx context.Context, externalID string) error {
	if err := dataio.DeleteManual(ctx, s.st, externalID); err != nil {
		return manualError(err)
	}
	s.budgetStateChanged()
	return nil
}

// ---- import ---------------------------------------------------------------

// RowErrorDTO is one rejected row of an import.
type RowErrorDTO struct {
	// Line is the 1-based line of a CSV file (the header is line 1) or the
	// position of the record in a JSON export.
	Line   int    `json:"line"`
	Reason string `json:"reason"`
}

// ImportResultDTO is the outcome of an import. Canceled is true when the user
// closed the file dialog; nothing else is set then.
type ImportResultDTO struct {
	Canceled  bool   `json:"canceled"`
	File      string `json:"file"` // base name of the imported file
	Format    string `json:"format"`
	Rows      int    `json:"rows"`
	Added     int    `json:"added"`
	Updated   int    `json:"updated"`
	Unchanged int    `json:"unchanged"`
	// Errors lists the rejected rows (at most 1000); ErrorCount is the full count.
	Errors     []RowErrorDTO `json:"errors"`
	ErrorCount int           `json:"errorCount"`
}

func importDTO(file, format string, r dataio.ImportResult) ImportResultDTO {
	out := ImportResultDTO{
		File: file, Format: format, Rows: r.Rows, Added: r.Added, Updated: r.Updated,
		Unchanged: r.Unchanged, ErrorCount: r.ErrorCount, Errors: make([]RowErrorDTO, 0, len(r.Errors)),
	}
	for _, e := range r.Errors {
		out.Errors = append(out.Errors, RowErrorDTO{Line: e.Line, Reason: e.Reason})
	}
	return out
}

func importError(err error, format string) error {
	switch {
	case errors.Is(err, dataio.ErrFileTooLarge):
		if format == FormatJSON {
			return fmt.Errorf("Dosya çok büyük (JSON için en fazla %d MiB).", dataio.MaxJSONImportBytes>>20)
		}
		return fmt.Errorf("Dosya çok büyük (CSV için en fazla %d MiB).", dataio.MaxCSVImportBytes>>20)
	case errors.Is(err, dataio.ErrEmptyFile):
		return errors.New("Dosya boş.")
	case errors.Is(err, dataio.ErrInvalidHeader):
		return fmt.Errorf("CSV başlığı geçersiz: %w", err)
	case errors.Is(err, dataio.ErrUnsupportedSchema):
		return errors.New("Bu dışa aktarma dosyasının sürümü desteklenmiyor.")
	case errors.Is(err, dataio.ErrInvalidExport):
		return errors.New("Dosya geçerli bir TokenMetrics JSON dışa aktarması değil.")
	}
	return err
}

// openImportFile opens a user-chosen file and refuses anything but a regular file.
func openImportFile(path string) (*os.File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("Dosya açılamadı: %w", err)
	}
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		_ = f.Close()
		return nil, errors.New("Seçilen yol normal bir dosya değil.")
	}
	return f, nil
}

// ImportCSVFile imports a usage CSV (docs/csv-format.md) from path.
func (s *Service) ImportCSVFile(ctx context.Context, path string) (ImportResultDTO, error) {
	f, err := openImportFile(path)
	if err != nil {
		return ImportResultDTO{}, err
	}
	defer f.Close()
	res, err := dataio.ImportCSV(ctx, s.st, f, dataio.CSVImportOptions{DeviceID: s.deviceID})
	if err != nil {
		return ImportResultDTO{}, importError(err, FormatCSV)
	}
	s.budgetStateChanged()
	return importDTO(filepath.Base(path), FormatCSV, res), nil
}

// ImportJSONFile merges a JSON export (possibly from another computer) from path.
func (s *Service) ImportJSONFile(ctx context.Context, path string) (ImportResultDTO, error) {
	f, err := openImportFile(path)
	if err != nil {
		return ImportResultDTO{}, err
	}
	defer f.Close()
	res, err := dataio.ImportJSON(ctx, s.st, f, s.deviceID)
	if err != nil {
		return ImportResultDTO{}, importError(err, FormatJSON)
	}
	s.budgetStateChanged()
	return importDTO(filepath.Base(path), FormatJSON, res), nil
}

// ---- export ---------------------------------------------------------------

// ExportResultDTO is the outcome of an export. Canceled is true when the user
// closed the file dialog.
type ExportResultDTO struct {
	Canceled bool   `json:"canceled"`
	Path     string `json:"path"`
	Format   string `json:"format"`
	Records  int64  `json:"records"`
}

// ExportFile writes the records matching f to path as csv or json. Settings
// and keys are never part of an export. A file that could not be written
// completely is removed.
func (s *Service) ExportFile(ctx context.Context, path, format string, f FilterDTO) (res ExportResultDTO, err error) {
	if format != FormatCSV && format != FormatJSON {
		return ExportResultDTO{}, fmt.Errorf("bilinmeyen biçim %q", format)
	}
	flt, err := toStoreFilter(f)
	if err != nil {
		return ExportResultDTO{}, err
	}
	// A name without extension gets one, but then the dialog's "replace?"
	// question did not cover the final name: never overwrite silently.
	if filepath.Ext(path) == "" {
		path += "." + format
		if _, serr := os.Lstat(path); serr == nil {
			return ExportResultDTO{}, fmt.Errorf("%s zaten var; farklı bir ad seçin ya da dosya adına uzantıyı kendiniz yazın", filepath.Base(path))
		}
	}
	totals, err := s.st.Totals(ctx, flt)
	if err != nil {
		return ExportResultDTO{}, err
	}

	// Write to a temporary file next to the target and move it into place when
	// complete, so a failed export neither leaves a partial file nor destroys
	// the file it was meant to replace.
	out, err := os.CreateTemp(filepath.Dir(path), ".tokenmetrics-export-*")
	if err != nil {
		return ExportResultDTO{}, fmt.Errorf("Dosya yazılamadı: %w", err)
	}
	tmp := out.Name()
	defer func() {
		if err != nil {
			_ = os.Remove(tmp)
		}
	}()
	w := bufio.NewWriter(out)
	if format == FormatJSON {
		err = dataio.ExportJSON(ctx, s.st, w, flt, s.deviceID)
	} else {
		err = dataio.ExportCSV(ctx, s.st, w, flt)
	}
	if err == nil {
		err = w.Flush()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		return ExportResultDTO{}, fmt.Errorf("Dışa aktarma başarısız: %w", err)
	}
	return ExportResultDTO{Path: path, Format: format, Records: totals.RecordCount}, nil
}

// ExportFileName is the suggested name in the save dialog.
func ExportFileName(format string, now time.Time) string {
	return "tokenmetrics-" + now.Format("2006-01-02") + "." + strings.ToLower(format)
}
