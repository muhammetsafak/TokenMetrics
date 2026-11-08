package tray

import "testing"

func TestIconSwitchAppliesOnlyChanges(t *testing.T) {
	var s iconSwitch

	// Before the tray is ready nothing is due, even with a value set.
	s.set(true)
	if _, ok := s.due(); ok {
		t.Fatal("due before activate")
	}

	// Once ready, the value set earlier is shown.
	s.activate()
	if alert, ok := s.due(); !ok || !alert {
		t.Fatalf("first due = (%v, %v), want (true, true)", alert, ok)
	}
	// The same value again is not set twice.
	s.set(true)
	if _, ok := s.due(); ok {
		t.Fatal("same value due again")
	}
	if _, ok := s.due(); ok {
		t.Fatal("due twice without a change")
	}

	s.set(false)
	if alert, ok := s.due(); !ok || alert {
		t.Fatalf("back to normal = (%v, %v), want (false, true)", alert, ok)
	}
	// A change and its undo before the tray looked at it is not due.
	s.set(true)
	s.set(false)
	if _, ok := s.due(); ok {
		t.Fatal("net no change was due")
	}

	// A stopped tray takes nothing.
	s.deactivate()
	s.set(true)
	if _, ok := s.due(); ok {
		t.Fatal("due after deactivate")
	}
}

func TestIconSwitchNormalIconShownFirst(t *testing.T) {
	var s iconSwitch
	s.activate()
	if alert, ok := s.due(); !ok || alert {
		t.Fatalf("first due = (%v, %v), want (false, true)", alert, ok)
	}
}

func TestOptionsIconFallsBack(t *testing.T) {
	o := Options{IconPNG: []byte("n"), IconICO: []byte("n")}
	if string(o.icon(true)) != "n" {
		t.Error("alert icon should fall back to the normal one")
	}
	if o.template(true) != nil {
		t.Error("no template configured")
	}
	o.TemplatePNG = []byte("t")
	if string(o.template(true)) != "t" {
		t.Error("template alert should fall back to the template")
	}
	o.TemplateAlertPNG = []byte("ta")
	if string(o.template(true)) != "ta" || string(o.template(false)) != "t" {
		t.Error("template variants")
	}
}
