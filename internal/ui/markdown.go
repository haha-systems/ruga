package ui

import (
	"strings"

	"github.com/charmbracelet/glamour"
	"github.com/charmbracelet/glamour/ansi"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

const minimumMarkdownWidth = 24

func renderAssistantMarkdown(text string, width int, theme Theme, colorSupported bool) (string, bool) {
	plain := wrapPreservingLines(safeTerminalText(text), max(1, width))
	if width < minimumMarkdownWidth || !colorSupported {
		return plain, false
	}

	if theme.MarkdownStyle == (ansi.StyleConfig{}) {
		theme.MarkdownStyle = defaultTheme.MarkdownStyle
	}

	renderer, err := glamour.NewTermRenderer(
		glamour.WithStyles(theme.MarkdownStyle),
		glamour.WithWordWrap(width),
	)
	if err != nil {
		return plain, false
	}

	rendered, err := renderer.Render(safeTerminalText(text))
	if err != nil {
		return plain, false
	}

	return strings.TrimSpace(rendered), true
}

func terminalSupportsMarkdownColor() bool {
	return lipgloss.ColorProfile() != termenv.Ascii
}
