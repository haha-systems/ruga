package ui

import (
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestPanelInsets(t *testing.T) {
	for _, test := range []struct {
		focused bool
		want    [2]int
	}{{false, [2]int{2, 2}}, {true, [2]int{4, 4}}} {
		horizontal, vertical := panelInsets(test.focused)
		got := [2]int{horizontal, vertical}
		if got != test.want {
			t.Fatalf("panelInsets(%t) = %v, want %v", test.focused, got, test.want)
		}
	}
}

func TestPanelStyle(t *testing.T) {
	theme := NewDefaultTheme()
	for _, test := range []struct {
		focused bool
		wantH   int
	}{{false, 3}, {true, 5}} {
		view := renderPanel(theme, "x", 10, test.focused)
		if got := lipgloss.Width(view); got != 10 {
			t.Fatalf("renderPanel width = %d, want 10", got)
		}

		if got := lipgloss.Height(view); got != test.wantH {
			t.Fatalf("renderPanel height = %d, want %d", got, test.wantH)
		}
	}
}
