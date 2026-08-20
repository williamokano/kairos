package tui

import (
	"reflect"
	"strings"
	"testing"
)

func TestPalette_resolvesScreenNamesVerbsAndULIDs(t *testing.T) {
	var m Model

	if s, _, isRun, matched := m.resolvePalette("inbox"); !matched || isRun || s != ScreenInbox {
		t.Fatalf("resolvePalette(inbox) = %v, %v, %v, want ScreenInbox, false, true", s, isRun, matched)
	}
	if s, _, isRun, matched := m.resolvePalette("bench"); !matched || isRun || s != ScreenBenchmark {
		t.Fatalf("resolvePalette(bench) = %v, %v, %v, want ScreenBenchmark, false, true", s, isRun, matched)
	}
	if s, _, isRun, matched := m.resolvePalette("approve"); !matched || isRun || s != ScreenInbox {
		t.Fatalf("resolvePalette(approve) = %v, %v, %v, want ScreenInbox verb mapping", s, isRun, matched)
	}

	ulid := "01ARZ3NDEKTSV4RRFFQ69G5FAV" // 26 chars, Crockford-shaped
	if s, id, isRun, matched := m.resolvePalette(ulid); !matched || !isRun || s != ScreenRunInspector || id != ulid {
		t.Fatalf("resolvePalette(ulid) = %v, %v, %v, %v, want ScreenRunInspector/true/true/%s", s, id, isRun, matched, ulid)
	}

	if _, _, _, matched := m.resolvePalette("totally-not-a-thing"); matched {
		t.Fatal("expected an unrecognised token to not match — no fuzzy search")
	}
	if _, _, _, matched := m.resolvePalette(""); matched {
		t.Fatal("expected empty input to not match")
	}
}

func TestPalette_noFuzzyMatching(t *testing.T) {
	var m Model
	// "inbo" is one character short of "inbox" — a fuzzy matcher would
	// resolve it; the real palette must not.
	if _, _, _, matched := m.resolvePalette("inbo"); matched {
		t.Fatal("expected a near-miss token to not match — fuzzy search is explicitly excluded")
	}
}

// TestPalette_noHistoryFeature asserts the negative directly, structurally:
// no field on Model (recursively, including decisionState/homeState/etc.)
// has a name suggesting a command-palette history list. If one is ever
// added, this test forces its author to explain why —
// 09-cli-and-tui.md is explicit that history is excluded, not merely
// unbuilt yet.
func TestPalette_noHistoryFeature(t *testing.T) {
	seen := map[reflect.Type]bool{}
	var walk func(t reflect.Type)
	walk = func(t reflect.Type) {
		if t.Kind() != reflect.Struct || seen[t] {
			return
		}
		seen[t] = true
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			lname := strings.ToLower(f.Name)
			if strings.Contains(lname, "history") || strings.Contains(lname, "recent") {
				panic("field " + t.Name() + "." + f.Name + " looks like a command-palette history feature, which 09-cli-and-tui.md explicitly excludes")
			}
			ft := f.Type
			if ft.Kind() == reflect.Ptr || ft.Kind() == reflect.Slice {
				ft = ft.Elem()
			}
			walk(ft)
		}
	}
	walk(reflect.TypeOf(Model{}))
}
