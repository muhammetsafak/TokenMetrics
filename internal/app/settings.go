package app

// Settings bindings: source configuration, Admin keys, prices, limits and
// budgets. Like the rest of this package it adds no business rules; it
// validates what the user typed (so the screen can show a clear message),
// forwards to the store / secrets / pricing / limits packages and shapes the
// result into DTOs.
//
// Admin keys flow in one direction only: SetAdminKey takes a key, nothing in
// this file ever returns one. Status DTOs carry the masked form
// (secrets.Masked) and error text is built from sentinel errors, never from
// the key.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/muhammetsafak/TokenMetrics/internal/collector/claudecode"
	"github.com/muhammetsafak/TokenMetrics/internal/collector/codex"
	"github.com/muhammetsafak/TokenMetrics/internal/collector/gemini"
	"github.com/muhammetsafak/TokenMetrics/internal/limits"
	"github.com/muhammetsafak/TokenMetrics/internal/model"
	"github.com/muhammetsafak/TokenMetrics/internal/pricing"
	"github.com/muhammetsafak/TokenMetrics/internal/secrets"
	"github.com/muhammetsafak/TokenMetrics/internal/store"
)

const (
	maxNameLen   = 256
	maxKeyLen    = 4096
	maxPriceMTok = 1e6  // USD per million tokens; far above any real price
	maxTokenSum  = 1e15 // same bound the data entry path uses
	maxUSDAmount = 1e9
)

// ---- Admin key storage ----------------------------------------------------

// KeyStore stores the Admin API keys. The default is the OS keychain
// (internal/secrets); tests plug in a fake.
type KeyStore interface {
	Set(provider, key string) error
	Get(provider string) (string, error)
	Delete(provider string) error
}

type keychain struct{}

func (keychain) Set(p, k string) error        { return secrets.Set(p, k) }
func (keychain) Get(p string) (string, error) { return secrets.Get(p) }
func (keychain) Delete(p string) error        { return secrets.Delete(p) }

// keyConfigured is the default Config.HasKey: a key is stored for provider.
func (s *Service) keyConfigured(provider string) (bool, error) {
	_, err := s.keys.Get(provider)
	if errors.Is(err, secrets.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

// keyErrorText turns a key-storage error into a Turkish message. It is built
// from sentinel errors only, so it cannot contain a key.
func keyErrorText(err error) string {
	switch {
	case errors.Is(err, secrets.ErrKeychainUnavailable):
		return "İşletim sistemi anahtar deposuna erişilemiyor. Linux'ta GNOME Keyring veya KWallet gibi bir Secret Service çalışıyor olmalı; Windows'ta Kimlik Bilgisi Yöneticisi, macOS'ta Anahtar Zinciri kullanılır."
	case errors.Is(err, secrets.ErrEmptyKey):
		return "Anahtar boş olamaz."
	case errors.Is(err, secrets.ErrInvalidProvider):
		return "Geçersiz sağlayıcı."
	default:
		return "Anahtar deposu hatası: " + err.Error()
	}
}

// ---- sources --------------------------------------------------------------

// SourceSettingDTO is one usage source with its configuration and the result
// of the last scan of this run.
type SourceSettingDTO struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	// Kind is local (log files), api (Admin API) or manual.
	Kind string `json:"kind"`
	// Collector is true when a collector scans this source; only those can be
	// switched on or off.
	Collector bool  `json:"collector"`
	Enabled   bool  `json:"enabled"`
	Active    bool  `json:"active"`
	Records   int64 `json:"records"`

	// Provider is the keychain provider of an API source ("" for others).
	Provider string `json:"provider"`
	HasKey   bool   `json:"hasKey"`

	// PathEditable is true for the log-directory sources.
	PathEditable  bool   `json:"pathEditable"`
	PathOverride  string `json:"pathOverride"` // "" = default location
	DefaultPath   string `json:"defaultPath"`
	EffectivePath string `json:"effectivePath"`
	PathExists    bool   `json:"pathExists"`

	// Last scan of this run of the application; LastStatus is "" before one ran.
	LastStatus   string   `json:"lastStatus"` // running | done | skipped | error
	LastError    string   `json:"lastError"`
	LastWarnings []string `json:"lastWarnings"`
	LastAdded    int      `json:"lastAdded"`
	LastUpdated  int      `json:"lastUpdated"`
	LastFiles    int      `json:"lastFiles"`
	LastScanMs   int64    `json:"lastScanMs"`
}

var sourcePathKeys = map[model.Source]string{
	model.SourceClaudeCode: claudecode.SettingRootPath,
	model.SourceCodexCLI:   codex.SettingRootPath,
	model.SourceGeminiCLI:  gemini.SettingRootPath,
}

type defaultRooter interface{ DefaultRoot() (string, error) }

func (s *Service) sourceByID(id string) (sourceInfo, error) {
	for _, info := range sources {
		if string(info.id) == id {
			return info, nil
		}
	}
	return sourceInfo{}, fmt.Errorf("bilinmeyen kaynak %q", id)
}

// SourceSettings lists every source with its switch, path and last scan state.
func (s *Service) SourceSettings(ctx context.Context) ([]SourceSettingDTO, error) {
	counts := map[string]int64{}
	rows, err := s.st.Breakdown(ctx, store.Filter{}, store.DimSource)
	if err != nil {
		return nil, err
	}
	for _, r := range rows {
		counts[r.Key] = r.RecordCount
	}
	_, _, last := s.scanState()
	steps := map[string]ScanStep{}
	var lastMs int64
	if last != nil {
		lastMs = last.FinishedMs
		for _, st := range last.Steps {
			steps[st.Source] = st
		}
	}

	out := make([]SourceSettingDTO, 0, len(sources))
	for _, info := range sources {
		c, hasCollector := s.registry.Get(info.id)
		dto := SourceSettingDTO{
			ID: string(info.id), Label: info.label, Kind: info.kind,
			Collector: hasCollector, Enabled: true,
			Records:      counts[string(info.id)],
			Provider:     info.provider,
			LastWarnings: []string{},
		}
		if hasCollector {
			active, enabled, err := s.sourceActive(ctx, info)
			if err != nil {
				return nil, err
			}
			dto.Active, dto.Enabled = active, enabled
		}
		if info.provider != "" {
			ok, kerr := s.hasKey(info.provider)
			dto.HasKey = kerr == nil && ok
		}
		if key, ok := sourcePathKeys[info.id]; ok && hasCollector {
			dto.PathEditable = true
			var p string
			if _, err := s.st.GetSetting(ctx, key, &p); err != nil {
				return nil, err
			}
			dto.PathOverride = p
			if dr, ok := c.(defaultRooter); ok {
				if root, err := dr.DefaultRoot(); err == nil {
					dto.DefaultPath = root
				}
			}
			dto.EffectivePath = dto.DefaultPath
			if p != "" {
				dto.EffectivePath = p
			}
			if dto.EffectivePath != "" {
				if fi, err := os.Stat(dto.EffectivePath); err == nil && fi.IsDir() {
					dto.PathExists = true
				}
			}
		}
		if st, ok := steps[dto.ID]; ok {
			dto.LastStatus, dto.LastError = st.Status, st.Error
			dto.LastWarnings = nonNil(st.Warnings)
			dto.LastAdded, dto.LastUpdated, dto.LastFiles = st.Added, st.Updated, st.Files
			dto.LastScanMs = lastMs
		}
		out = append(out, dto)
	}
	return out, nil
}

// SetSourceEnabled switches a scanned source on or off. A source that is off
// is skipped by every scan; its stored records stay.
func (s *Service) SetSourceEnabled(ctx context.Context, id string, enabled bool) error {
	info, err := s.sourceByID(id)
	if err != nil {
		return err
	}
	if _, ok := s.registry.Get(info.id); !ok {
		return fmt.Errorf("%s taranan bir kaynak değil", info.label)
	}
	return s.st.SetSetting(ctx, "sources."+id+".enabled", enabled)
}

// SetSourcePath makes dir the log directory of a local source. The path comes
// from a directory dialog (see App.PickSourcePath), never from free text.
func (s *Service) SetSourcePath(ctx context.Context, id, dir string) error {
	info, err := s.sourceByID(id)
	if err != nil {
		return err
	}
	key, ok := sourcePathKeys[info.id]
	if !ok {
		return fmt.Errorf("%s için klasör yolu ayarlanamaz", info.label)
	}
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return errors.New("klasör yolu boş")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return fmt.Errorf("klasör yolu çözümlenemedi: %w", err)
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return fmt.Errorf("klasöre erişilemiyor: %w", err)
	}
	if !fi.IsDir() {
		return errors.New("seçilen yol bir klasör değil")
	}
	return s.st.SetSetting(ctx, key, abs)
}

// ResetSourcePath goes back to the default log directory of a local source.
func (s *Service) ResetSourcePath(ctx context.Context, id string) error {
	info, err := s.sourceByID(id)
	if err != nil {
		return err
	}
	key, ok := sourcePathKeys[info.id]
	if !ok {
		return fmt.Errorf("%s için klasör yolu ayarlanamaz", info.label)
	}
	return s.st.DeleteSetting(ctx, key)
}

// ---- Admin keys -----------------------------------------------------------

// AdminKeyDTO is the state of one provider's Admin key. The key itself never
// appears: Masked shows only its last four characters.
type AdminKeyDTO struct {
	Provider   string `json:"provider"`
	Label      string `json:"label"`
	Source     string `json:"source"`
	Configured bool   `json:"configured"`
	Masked     string `json:"masked"`
	// Error is set when the keychain could not be read for this provider.
	Error string `json:"error"`
}

// AdminKeysDTO lists the providers that take an Admin key.
type AdminKeysDTO struct {
	Keys []AdminKeyDTO `json:"keys"`
	// KeychainError is set when the OS keychain is unusable (typically Linux
	// without a Secret Service); keys cannot be stored then.
	KeychainError string `json:"keychainError"`
}

func (s *Service) adminKeyState(provider string, info sourceInfo) (AdminKeyDTO, bool) {
	dto := AdminKeyDTO{Provider: provider, Label: info.label, Source: string(info.id)}
	v, err := s.keys.Get(provider)
	switch {
	case err == nil:
		dto.Configured, dto.Masked = true, secrets.Masked(v)
	case errors.Is(err, secrets.ErrNotFound):
	default:
		dto.Error = keyErrorText(err)
		return dto, errors.Is(err, secrets.ErrKeychainUnavailable)
	}
	return dto, false
}

// AdminKeys reports which providers have a key stored, in masked form.
func (s *Service) AdminKeys() AdminKeysDTO {
	out := AdminKeysDTO{Keys: []AdminKeyDTO{}}
	for _, info := range sources {
		if info.provider == "" {
			continue
		}
		dto, unavailable := s.adminKeyState(info.provider, info)
		if unavailable {
			out.KeychainError = dto.Error
		}
		out.Keys = append(out.Keys, dto)
	}
	return out
}

func apiSource(provider string) (sourceInfo, error) {
	for _, info := range sources {
		if info.provider != "" && info.provider == provider {
			return info, nil
		}
	}
	return sourceInfo{}, errors.New("Admin key yalnız Anthropic ve OpenAI için saklanır")
}

// SetAdminKey stores key for provider in the OS keychain and returns the
// masked state. The key is never echoed back, not even in an error.
func (s *Service) SetAdminKey(provider, key string) (AdminKeyDTO, error) {
	info, err := apiSource(provider)
	if err != nil {
		return AdminKeyDTO{}, err
	}
	key = strings.TrimSpace(key)
	switch {
	case key == "":
		return AdminKeyDTO{}, errors.New(keyErrorText(secrets.ErrEmptyKey))
	case len(key) > maxKeyLen:
		return AdminKeyDTO{}, errors.New("Anahtar çok uzun.")
	case strings.ContainsAny(key, " \t\r\n"):
		return AdminKeyDTO{}, errors.New("Anahtar boşluk veya satır sonu içeremez; yalnız anahtarı yapıştırın.")
	}
	if err := s.keys.Set(provider, key); err != nil {
		// Belt and braces: a storage layer that quoted the key in its error
		// must not get it onto the screen.
		return AdminKeyDTO{}, errors.New(strings.ReplaceAll(keyErrorText(err), key, "***"))
	}
	dto, _ := s.adminKeyState(provider, info)
	return dto, nil
}

// DeleteAdminKey removes the stored key of provider. A missing key is fine.
func (s *Service) DeleteAdminKey(provider string) (AdminKeyDTO, error) {
	info, err := apiSource(provider)
	if err != nil {
		return AdminKeyDTO{}, err
	}
	if err := s.keys.Delete(provider); err != nil {
		return AdminKeyDTO{}, errors.New(keyErrorText(err))
	}
	dto, _ := s.adminKeyState(provider, info)
	return dto, nil
}

// ---- prices ---------------------------------------------------------------

// PriceSetDTO is a price in USD per one million tokens.
type PriceSetDTO struct {
	Input        float64 `json:"input"`
	Output       float64 `json:"output"`
	CacheRead    float64 `json:"cacheRead"`
	CacheWrite5m float64 `json:"cacheWrite5m"`
	CacheWrite1h float64 `json:"cacheWrite1h"`
}

func priceSet(p pricing.Price) *PriceSetDTO {
	return &PriceSetDTO{p.Input, p.Output, p.CacheRead, p.CacheWrite5m, p.CacheWrite1h}
}

// PriceOverrideDTO is a user price for one model. A null component means "use
// the built-in price".
type PriceOverrideDTO struct {
	Model        string   `json:"model"`
	Input        *float64 `json:"input"`
	Output       *float64 `json:"output"`
	CacheRead    *float64 `json:"cacheRead"`
	CacheWrite5m *float64 `json:"cacheWrite5m"`
	CacheWrite1h *float64 `json:"cacheWrite1h"`
}

func (d PriceOverrideDTO) components() []*float64 {
	return []*float64{d.Input, d.Output, d.CacheRead, d.CacheWrite5m, d.CacheWrite1h}
}

func overrideDTO(o store.PriceOverride) *PriceOverrideDTO {
	return &PriceOverrideDTO{
		Model: o.Model, Input: o.InputPerMTok, Output: o.OutputPerMTok, CacheRead: o.CacheReadPerMTok,
		CacheWrite5m: o.CacheWrite5mPerMTok, CacheWrite1h: o.CacheWrite1hPerMTok,
	}
}

// PriceRowDTO is one model of the price table.
type PriceRowDTO struct {
	Model   string `json:"model"`
	Service string `json:"service"`
	Source  string `json:"source"`
	// Custom marks a model that is not in the built-in table; it exists only
	// as a user override.
	Custom     bool `json:"custom"`
	Overridden bool `json:"overridden"`
	// Default is the built-in price (absent for a custom model).
	Default *PriceSetDTO `json:"default"`
	// Effective is what costs are computed with (absent when a custom model's
	// override lacks the input or output price, which makes it unpriced).
	Effective *PriceSetDTO `json:"effective"`
	// Override holds the user's components (absent when there is none).
	Override *PriceOverrideDTO `json:"override"`
	// LongPrompt is the effective price for prompts over the threshold.
	LongPrompt *PriceSetDTO `json:"longPrompt"`
	ValidUntil string       `json:"validUntil"`
	Expired    bool         `json:"expired"`
}

// PricesDTO is the price table with the user's overrides applied.
type PricesDTO struct {
	Rows                []PriceRowDTO `json:"rows"`
	AsOf                string        `json:"asOf"`
	LongPromptThreshold int64         `json:"longPromptThreshold"`
}

// Prices returns the built-in table and the custom models, with the effective
// price of each.
func (s *Service) Prices(ctx context.Context) (PricesDTO, error) {
	overrides, err := s.st.ListPriceOverrides(ctx)
	if err != nil {
		return PricesDTO{}, err
	}
	p, err := pricing.New(overrides)
	if err != nil {
		return PricesDTO{}, err
	}
	byKey := make(map[string]store.PriceOverride, len(overrides))
	for _, o := range overrides {
		byKey[strings.ToLower(strings.TrimSpace(o.Model))] = o
	}
	now := s.now()
	out := PricesDTO{Rows: []PriceRowDTO{}, AsOf: p.AsOf(), LongPromptThreshold: pricing.LongPromptThreshold}

	seen := map[string]bool{}
	for _, d := range p.ListDefaults() {
		seen[d.Model] = true
		row := PriceRowDTO{
			Model: d.Model, Service: d.Service, Source: d.Source,
			Default: priceSet(d.Price), ValidUntil: d.ValidUntil,
		}
		if o, ok := byKey[d.Model]; ok {
			row.Override = overrideDTO(o)
			row.Override.Model = d.Model
		}
		if res, ok := p.Lookup(d.Model); ok {
			row.Effective = priceSet(res.Price)
			row.Overridden = res.Overridden
			row.Expired = res.Expired(now)
			if res.Long != nil {
				row.LongPrompt = priceSet(*res.Long)
			}
		}
		out.Rows = append(out.Rows, row)
	}
	for _, o := range overrides { // already ordered by model
		key := strings.ToLower(strings.TrimSpace(o.Model))
		if seen[key] {
			continue
		}
		row := PriceRowDTO{Model: o.Model, Custom: true, Overridden: true, Override: overrideDTO(o)}
		if res, ok := p.Lookup(o.Model); ok {
			row.Effective = priceSet(res.Price)
		}
		out.Rows = append(out.Rows, row)
	}
	return out, nil
}

// SetPriceOverride saves the user's price for a model, replacing an earlier
// one. At least one component must be set.
func (s *Service) SetPriceOverride(ctx context.Context, d PriceOverrideDTO) error {
	name := strings.TrimSpace(d.Model)
	switch {
	case name == "":
		return errors.New("model adı boş olamaz")
	case utf8.RuneCountInString(name) > maxNameLen:
		return fmt.Errorf("model adı %d karakterden uzun olamaz", maxNameLen)
	}
	hasComponent := false
	for _, c := range d.components() {
		if c == nil {
			continue
		}
		hasComponent = true
		if math.IsNaN(*c) || math.IsInf(*c, 0) || *c < 0 || *c > maxPriceMTok {
			return fmt.Errorf("fiyat 0 ile %.0f USD arasında olmalı", maxPriceMTok)
		}
	}
	if !hasComponent {
		return errors.New("en az bir fiyat alanı doldurulmalı")
	}
	if err := s.st.SetPriceOverride(ctx, store.PriceOverride{
		Model: name, InputPerMTok: d.Input, OutputPerMTok: d.Output, CacheReadPerMTok: d.CacheRead,
		CacheWrite5mPerMTok: d.CacheWrite5m, CacheWrite1hPerMTok: d.CacheWrite1h,
	}); err != nil {
		return err
	}
	s.budgetStateChanged() // USD budgets are priced with it
	return nil
}

// ResetPriceOverride removes the user's price of a model (a custom model
// disappears from the table).
func (s *Service) ResetPriceOverride(ctx context.Context, name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("model adı boş olamaz")
	}
	if err := s.st.DeletePriceOverride(ctx, name); err != nil {
		return err
	}
	s.budgetStateChanged()
	return nil
}

// ---- limits ---------------------------------------------------------------

// LimitSettingDTO is one configured limit window. Limit is a token count for
// the token metrics and an amount in USD for cost_usd (the store keeps cents).
type LimitSettingDTO struct {
	ID      int64   `json:"id"`
	Service string  `json:"service"` // "" = all services
	Kind    string  `json:"kind"`    // 5h | weekly
	Metric  string  `json:"metric"`  // total_tokens | output_tokens | cost_usd
	Limit   float64 `json:"limit"`
}

// WeeklyStartDTO says when the weekly window begins.
type WeeklyStartDTO struct {
	Weekday  int    `json:"weekday"`  // 0 = Sunday ... 6 = Saturday
	Hour     int    `json:"hour"`     // 0-23
	Timezone string `json:"timezone"` // IANA name; "" = this computer's zone
}

// LimitSettingsDTO is the data of the limit settings tab.
type LimitSettingsDTO struct {
	Limits      []LimitSettingDTO `json:"limits"`
	WeeklyStart WeeklyStartDTO    `json:"weeklyStart"`
}

func limitDTO(l store.LimitWindow) LimitSettingDTO {
	v := float64(l.Limit)
	if l.Metric == store.MetricCostUSD {
		v /= 100
	}
	return LimitSettingDTO{ID: l.ID, Service: l.Service, Kind: l.Kind, Metric: l.Metric, Limit: v}
}

// LimitSettings returns the configured limits and the weekly window start.
func (s *Service) LimitSettings(ctx context.Context) (LimitSettingsDTO, error) {
	ws, err := limits.New(s.st, nil).WeeklyStartSetting(ctx)
	if err != nil {
		return LimitSettingsDTO{}, err
	}
	list, err := s.st.ListLimitWindows(ctx)
	if err != nil {
		return LimitSettingsDTO{}, err
	}
	out := LimitSettingsDTO{
		Limits:      make([]LimitSettingDTO, 0, len(list)),
		WeeklyStart: WeeklyStartDTO{Weekday: ws.Weekday, Hour: ws.Hour, Timezone: ws.Timezone},
	}
	for _, l := range list {
		out.Limits = append(out.Limits, limitDTO(l))
	}
	return out, nil
}

// SaveLimit creates the limit for (service, kind) or replaces the existing
// one: there is at most one limit per service and kind. With a non-zero ID
// that limit is edited instead: its service and window must not change.
func (s *Service) SaveLimit(ctx context.Context, d LimitSettingDTO) (LimitSettingDTO, error) {
	svc := strings.TrimSpace(d.Service)
	if utf8.RuneCountInString(svc) > maxNameLen {
		return LimitSettingDTO{}, fmt.Errorf("servis adı %d karakterden uzun olamaz", maxNameLen)
	}
	switch d.Kind {
	case store.LimitKind5h, store.LimitKindWeekly:
	default:
		return LimitSettingDTO{}, errors.New("pencere türü 5 saatlik veya haftalık olmalı")
	}
	var amount int64
	switch d.Metric {
	case store.MetricTotalTokens, store.MetricOutputTokens:
		if math.IsNaN(d.Limit) || d.Limit < 1 || d.Limit > maxTokenSum {
			return LimitSettingDTO{}, errors.New("token limiti 1 ile 10^15 arasında olmalı")
		}
		amount = int64(math.Round(d.Limit))
	case store.MetricCostUSD:
		if math.IsNaN(d.Limit) || d.Limit < 0.01 || d.Limit > maxUSDAmount {
			return LimitSettingDTO{}, errors.New("maliyet limiti en az 0,01 USD olmalı")
		}
		amount = int64(math.Round(d.Limit * 100))
	default:
		return LimitSettingDTO{}, errors.New("bilinmeyen ölçüt")
	}
	if d.ID != 0 {
		// An edit changes the value of one existing limit. Its service and
		// window are its identity (the store keeps one limit per pair), so
		// changing them would silently replace another limit.
		list, err := s.st.ListLimitWindows(ctx)
		if err != nil {
			return LimitSettingDTO{}, err
		}
		found := false
		for _, l := range list {
			if l.ID != d.ID {
				continue
			}
			found = true
			if l.Service != svc || l.Kind != d.Kind {
				return LimitSettingDTO{}, errors.New("düzenlerken servis ve pencere değiştirilemez; limiti silip yeniden ekleyin")
			}
		}
		if !found {
			return LimitSettingDTO{}, errors.New("limit bulunamadı; başka bir yerde silinmiş olabilir")
		}
	}
	lw := store.LimitWindow{Service: svc, Kind: d.Kind, Metric: d.Metric, Limit: amount}
	id, err := s.st.SetLimitWindow(ctx, lw)
	if err != nil {
		return LimitSettingDTO{}, err
	}
	lw.ID = id
	return limitDTO(lw), nil
}

// DeleteLimit removes a limit window.
func (s *Service) DeleteLimit(ctx context.Context, id int64) error {
	return s.st.DeleteLimitWindow(ctx, id)
}

// SetWeeklyStart saves when the weekly window begins.
func (s *Service) SetWeeklyStart(ctx context.Context, d WeeklyStartDTO) (WeeklyStartDTO, error) {
	d.Timezone = strings.TrimSpace(d.Timezone)
	ws := limits.WeeklyStart{Weekday: d.Weekday, Hour: d.Hour, Timezone: d.Timezone}
	if d.Weekday < 0 || d.Weekday > 6 {
		return WeeklyStartDTO{}, errors.New("gün 0-6 arasında olmalı")
	}
	if d.Hour < 0 || d.Hour > 23 {
		return WeeklyStartDTO{}, errors.New("saat 0-23 arasında olmalı")
	}
	if _, err := ws.Location(); err != nil {
		return WeeklyStartDTO{}, fmt.Errorf("saat dilimi tanınmıyor: %q (örnek: Europe/Istanbul)", d.Timezone)
	}
	if err := s.st.SetSetting(ctx, limits.SettingWeeklyStart, ws); err != nil {
		return WeeklyStartDTO{}, err
	}
	return d, nil
}

// ---- budgets --------------------------------------------------------------

// BudgetDTO is a spending or usage rule. At least one threshold is set; when
// both are, reaching either one is a breach.
type BudgetDTO struct {
	ID              int64    `json:"id"` // 0 = not saved yet
	Scope           string   `json:"scope"`
	ScopeValue      string   `json:"scopeValue"`
	Period          string   `json:"period"`
	ThresholdTokens *int64   `json:"thresholdTokens"`
	ThresholdUSD    *float64 `json:"thresholdUsd"`
	Enabled         bool     `json:"enabled"`
}

func budgetDTO(b store.Budget) BudgetDTO {
	return BudgetDTO{
		ID: b.ID, Scope: b.Scope, ScopeValue: b.ScopeValue, Period: b.Period,
		ThresholdTokens: b.ThresholdTokens, ThresholdUSD: b.ThresholdUSD, Enabled: b.Enabled,
	}
}

// Budgets lists the budget rules.
func (s *Service) Budgets(ctx context.Context) ([]BudgetDTO, error) {
	list, err := s.st.ListBudgets(ctx, false)
	if err != nil {
		return nil, err
	}
	out := make([]BudgetDTO, 0, len(list))
	for _, b := range list {
		out = append(out, budgetDTO(b))
	}
	return out, nil
}

// ErrBudgetNotFound is returned when a budget to update does not exist.
var ErrBudgetNotFound = errors.New("bütçe bulunamadı")

// SaveBudget creates a budget (ID 0) or replaces the one with that ID, and
// returns it as stored.
func (s *Service) SaveBudget(ctx context.Context, d BudgetDTO) (BudgetDTO, error) {
	b := store.Budget{
		ID: d.ID, Scope: d.Scope, ScopeValue: strings.TrimSpace(d.ScopeValue), Period: d.Period,
		ThresholdTokens: d.ThresholdTokens, ThresholdUSD: d.ThresholdUSD, Enabled: d.Enabled,
	}
	switch b.Scope {
	case store.BudgetScopeTotal:
		b.ScopeValue = ""
	case store.BudgetScopeService, store.BudgetScopeModel:
		if b.ScopeValue == "" {
			return BudgetDTO{}, errors.New("servis veya model adı girilmeli")
		}
		if utf8.RuneCountInString(b.ScopeValue) > maxNameLen {
			return BudgetDTO{}, fmt.Errorf("ad %d karakterden uzun olamaz", maxNameLen)
		}
	default:
		return BudgetDTO{}, errors.New("bilinmeyen kapsam")
	}
	switch b.Period {
	case store.BudgetPeriodDaily, store.BudgetPeriodMonthly:
	default:
		return BudgetDTO{}, errors.New("dönem günlük veya aylık olmalı")
	}
	if b.ThresholdTokens == nil && b.ThresholdUSD == nil {
		return BudgetDTO{}, errors.New("token veya USD eşiklerinden en az biri girilmeli")
	}
	if b.ThresholdTokens != nil && (*b.ThresholdTokens < 1 || *b.ThresholdTokens > int64(maxTokenSum)) {
		return BudgetDTO{}, errors.New("token eşiği 1 ile 10^15 arasında olmalı")
	}
	if b.ThresholdUSD != nil && (math.IsNaN(*b.ThresholdUSD) || *b.ThresholdUSD < 0.01 || *b.ThresholdUSD > maxUSDAmount) {
		return BudgetDTO{}, errors.New("USD eşiği en az 0,01 olmalı")
	}
	if b.ID == 0 {
		id, err := s.st.CreateBudget(ctx, b)
		if err != nil {
			return BudgetDTO{}, err
		}
		b.ID = id
		s.budgetStateChanged()
		return budgetDTO(b), nil
	}
	if err := s.st.UpdateBudget(ctx, b); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return BudgetDTO{}, ErrBudgetNotFound
		}
		return BudgetDTO{}, err
	}
	s.budgetStateChanged()
	return budgetDTO(b), nil
}

// DeleteBudget removes a budget.
func (s *Service) DeleteBudget(ctx context.Context, id int64) error {
	if err := s.st.DeleteBudget(ctx, id); err != nil {
		return err
	}
	s.budgetStateChanged()
	return nil
}

// SetBudgetStateObserver registers fn to be called after something changed
// that can move a budget over or under its threshold (a budget was created,
// changed or deleted; usage records or prices changed), so the budget state
// can be re-evaluated without waiting for the next scan. nil removes the observer. fn must not block.
func (s *Service) SetBudgetStateObserver(fn func()) {
	s.mu.Lock()
	s.onBudgetState = fn
	s.mu.Unlock()
}

func (s *Service) budgetStateChanged() {
	s.mu.Lock()
	fn := s.onBudgetState
	s.mu.Unlock()
	if fn != nil {
		fn()
	}
}
