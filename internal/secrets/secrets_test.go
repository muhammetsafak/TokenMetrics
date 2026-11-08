package secrets

import (
	"errors"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
)

func TestSetGetDelete(t *testing.T) {
	keyring.MockInit()
	const key = "sk-ant-admin01-FAKEKEY1234567890abcd"

	if _, err := Get(ProviderAnthropic); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get before Set = %v, want ErrNotFound", err)
	}
	if err := Set(ProviderAnthropic, key); err != nil {
		t.Fatal(err)
	}
	got, err := Get(ProviderAnthropic)
	if err != nil || got != key {
		t.Fatalf("Get = %q, %v", got, err)
	}
	if ok, err := Has(ProviderAnthropic); !ok || err != nil {
		t.Fatalf("Has = %v, %v", ok, err)
	}
	// Providers are independent.
	if _, err := Get(ProviderOpenAI); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other provider = %v, want ErrNotFound", err)
	}
	if err := Delete(ProviderAnthropic); err != nil {
		t.Fatal(err)
	}
	if _, err := Get(ProviderAnthropic); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after Delete = %v", err)
	}
	if err := Delete(ProviderAnthropic); err != nil {
		t.Fatalf("second Delete = %v, want nil", err)
	}
	if ok, err := Has(ProviderAnthropic); ok || err != nil {
		t.Fatalf("Has after Delete = %v, %v", ok, err)
	}
}

func TestSetReplaces(t *testing.T) {
	keyring.MockInit()
	if err := Set(ProviderOpenAI, "first-key-1234567890"); err != nil {
		t.Fatal(err)
	}
	if err := Set(ProviderOpenAI, "second-key-1234567890"); err != nil {
		t.Fatal(err)
	}
	if got, _ := Get(ProviderOpenAI); got != "second-key-1234567890" {
		t.Fatalf("got %q", got)
	}
}

func TestValidation(t *testing.T) {
	keyring.MockInit()
	for _, p := range []string{"", "Anthropic", "a b", "../x", strings.Repeat("a", 65)} {
		if err := Set(p, "k"); !errors.Is(err, ErrInvalidProvider) {
			t.Errorf("Set(%q) = %v, want ErrInvalidProvider", p, err)
		}
		if _, err := Get(p); !errors.Is(err, ErrInvalidProvider) {
			t.Errorf("Get(%q) = %v", p, err)
		}
		if err := Delete(p); !errors.Is(err, ErrInvalidProvider) {
			t.Errorf("Delete(%q) = %v", p, err)
		}
	}
	if err := Set(ProviderOpenAI, "  \t"); !errors.Is(err, ErrEmptyKey) {
		t.Errorf("blank key = %v, want ErrEmptyKey", err)
	}
}

func TestKeychainErrorDoesNotLeakKey(t *testing.T) {
	keyring.MockInitWithError(errors.New("dbus: no session bus"))
	const key = "sk-ant-admin01-FAKEKEY1234567890abcd"
	err := Set(ProviderAnthropic, key)
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), key) {
		t.Fatalf("error leaks key: %v", err)
	}
	if _, err := Get(ProviderAnthropic); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("Get = %v, want a keychain error", err)
	}
}

func TestClassify(t *testing.T) {
	boom := errors.New("dbus: connection refused")
	if err := classify("linux", boom); !errors.Is(err, ErrKeychainUnavailable) {
		t.Errorf("linux = %v, want ErrKeychainUnavailable", err)
	}
	if err := classify("windows", boom); errors.Is(err, ErrKeychainUnavailable) {
		t.Errorf("windows = %v, must not be ErrKeychainUnavailable", err)
	}
	if err := classify("linux", keyring.ErrNotFound); !errors.Is(err, ErrNotFound) {
		t.Errorf("not found = %v", err)
	}
	if err := classify("darwin", keyring.ErrUnsupportedPlatform); !errors.Is(err, ErrKeychainUnavailable) {
		t.Errorf("unsupported = %v", err)
	}
	if err := classify("linux", nil); err != nil {
		t.Errorf("nil = %v", err)
	}
}

func TestMasked(t *testing.T) {
	cases := map[string]string{
		"":                                "",
		"abc":                             "***",
		"12345678901":                     "***********",
		"sk-ant-admin01-FAKEKEY12345abcd": "********abcd",
	}
	for in, want := range cases {
		if got := Masked(in); got != want {
			t.Errorf("Masked(%q) = %q, want %q", in, got, want)
		}
	}
	m := Masked("sk-ant-admin01-SECRETMIDDLEPART-wxyz")
	if strings.Contains(m, "SECRET") || !strings.HasSuffix(m, "wxyz") {
		t.Errorf("Masked leaks: %q", m)
	}
}

func TestSetTrimsWhitespace(t *testing.T) {
	keyring.MockInit()
	if err := Set(ProviderOpenAI, "  sk-admin-FAKEKEY-1234567890\r\n"); err != nil {
		t.Fatal(err)
	}
	if got, _ := Get(ProviderOpenAI); got != "sk-admin-FAKEKEY-1234567890" {
		t.Fatalf("stored %q, want trimmed value", got)
	}
}
