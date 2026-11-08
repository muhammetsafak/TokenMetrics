// Package secrets stores provider Admin API keys in the operating system
// keychain (see docs/design.md): Windows Credential Manager, macOS Keychain or the Linux
// Secret Service. Keys are never written to disk by this application.
package secrets

import (
	"errors"
	"fmt"
	"regexp"
	"runtime"
	"strings"

	"github.com/zalando/go-keyring"
)

// ServiceName is the keychain service name every entry is stored under.
const ServiceName = "TokenMetrics"

// Provider names used by the built-in collectors.
const (
	ProviderAnthropic = "anthropic"
	ProviderOpenAI    = "openai"
)

var (
	// ErrNotFound means no key is stored for the provider.
	ErrNotFound = errors.New("secrets: no key stored for provider")
	// ErrKeychainUnavailable means the OS keychain cannot be used, typically a
	// Linux session without a running Secret Service (GNOME Keyring, KWallet).
	ErrKeychainUnavailable = errors.New("secrets: OS keychain is unavailable (on Linux a Secret Service such as GNOME Keyring or KWallet must be running)")
	// ErrInvalidProvider is returned for an empty or malformed provider name.
	ErrInvalidProvider = errors.New("secrets: invalid provider name")
	// ErrEmptyKey is returned when asked to store an empty key.
	ErrEmptyKey = errors.New("secrets: key is empty")
)

var providerRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// classify maps a go-keyring error to this package's errors. go-keyring errors
// never carry the stored value, so wrapping their message cannot leak a key.
func classify(goos string, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, keyring.ErrNotFound):
		return ErrNotFound
	case errors.Is(err, keyring.ErrSetDataTooBig):
		return fmt.Errorf("secrets: key is too large for the OS keychain: %w", err)
	case errors.Is(err, keyring.ErrUnsupportedPlatform):
		return fmt.Errorf("%w: %v", ErrKeychainUnavailable, err)
	case goos == "linux":
		// On Linux every other failure comes from reaching the Secret Service
		// over D-Bus (no session bus, no provider, locked and no prompt).
		return fmt.Errorf("%w: %v", ErrKeychainUnavailable, err)
	default:
		return fmt.Errorf("secrets: keychain error: %w", err)
	}
}

func check(provider string) error {
	if !providerRe.MatchString(provider) {
		return fmt.Errorf("%w: %q", ErrInvalidProvider, provider)
	}
	return nil
}

// Set stores key for provider, replacing any existing key.
func Set(provider, key string) error {
	if err := check(provider); err != nil {
		return err
	}
	// Pasted keys often carry a trailing newline or spaces, which would later
	// make the HTTP header invalid; store the trimmed value.
	key = strings.TrimSpace(key)
	if key == "" {
		return ErrEmptyKey
	}
	return classify(runtime.GOOS, keyring.Set(ServiceName, provider, key))
}

// Get returns the key stored for provider, or ErrNotFound.
func Get(provider string) (string, error) {
	if err := check(provider); err != nil {
		return "", err
	}
	v, err := keyring.Get(ServiceName, provider)
	if err != nil {
		return "", classify(runtime.GOOS, err)
	}
	return v, nil
}

// Delete removes the key stored for provider. A missing key is not an error.
func Delete(provider string) error {
	if err := check(provider); err != nil {
		return err
	}
	err := classify(runtime.GOOS, keyring.Delete(ServiceName, provider))
	if errors.Is(err, ErrNotFound) {
		return nil
	}
	return err
}

// Has reports whether a key is stored for provider.
func Has(provider string) (bool, error) {
	_, err := Get(provider)
	if errors.Is(err, ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

// minMaskedLen is the shortest key of which the last four characters may be
// shown; shorter values are fully masked so the mask does not reveal them.
const minMaskedLen = 12

// Masked returns a display form of key that shows only its last four
// characters, e.g. "********abcd". Short keys are fully masked.
func Masked(key string) string {
	r := []rune(key)
	if len(r) == 0 {
		return ""
	}
	if len(r) < minMaskedLen {
		return strings.Repeat("*", len(r))
	}
	return strings.Repeat("*", 8) + string(r[len(r)-4:])
}
