package ui

import "github.com/charmbracelet/lipgloss"

func panelInsets(focused bool) (horizontal, vertical int) {
	horizontal, vertical = 2, 2
	if focused {
		horizontal, vertical = horizontal+2, vertical+2
	}

	return horizontal, vertical
}

func panelStyle(theme Theme, focused bool) lipgloss.Style {
	if focused {
		return theme.FocusedPanel
	}

	return theme.Panel
}

func renderPanel(theme Theme, content string, width int, focused bool) string {
	borderWidth := 0
	if focused {
		borderWidth = 2
	}

	return panelStyle(theme, focused).Width(max(1, width-borderWidth)).Render(content)
}
