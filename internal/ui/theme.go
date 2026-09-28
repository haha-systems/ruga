package ui

import (
	"github.com/charmbracelet/glamour/ansi"
	"github.com/charmbracelet/glamour/styles"
	"github.com/charmbracelet/lipgloss"

	"github.com/haha-systems/ruga/internal/presentation"
)

// Theme is the only place that maps presentation meaning to terminal styles.
// Additional themes can use the same roles without changing event renderers.
type Theme struct {
	BaseStyle     lipgloss.Style
	Roles         map[presentation.SemanticRole]lipgloss.Style
	Canvas        lipgloss.Style
	Text          lipgloss.Style
	Title         lipgloss.Style
	Divider       lipgloss.Style
	Panel         lipgloss.Style
	FocusedPanel  lipgloss.Style
	Composer      lipgloss.Style
	MarkdownStyle ansi.StyleConfig
}

var defaultTheme = NewDefaultTheme()

func NewDefaultTheme() Theme {
	const (
		background = lipgloss.Color("#10151E")
		text       = lipgloss.Color("#D9E1EA")
		muted      = lipgloss.Color("#595959")
		cyan       = lipgloss.Color("#75D9E9")
		green      = lipgloss.Color("#9AD7A5")
		amber      = lipgloss.Color("#E5BE80")
		violet     = lipgloss.Color("#B5A3EB")
		red        = lipgloss.Color("#EA8D97")
		line       = lipgloss.Color("#465264")
	)

	baseStyle := lipgloss.Style{}

	markdownStyle := styles.DarkStyleConfig
	markdownStyle.Document.Color = colorValue("#D9E1EA")
	markdownStyle.Heading.Color = colorValue("#75D9E9")
	markdownStyle.H1.Color = colorValue("#75D9E9")
	markdownStyle.H1.BackgroundColor = nil
	markdownStyle.H2.Color = colorValue("#75D9E9")
	markdownStyle.H3.Color = colorValue("#75D9E9")
	markdownStyle.Link.Color = colorValue("#75D9E9")
	markdownStyle.Code.Color = colorValue("#E5BE80")
	markdownStyle.Code.BackgroundColor = nil
	markdownStyle.CodeBlock.StylePrimitive.Color = colorValue("#D9E1EA")

	return Theme{
		BaseStyle: baseStyle,
		Roles: map[presentation.SemanticRole]lipgloss.Style{
			presentation.RoleNavigation: baseStyle.Foreground(cyan),
			presentation.RoleRead:       baseStyle.Foreground(cyan),
			presentation.RoleMutation:   baseStyle.Foreground(violet),
			presentation.RoleExecution:  baseStyle.Foreground(amber),
			presentation.RoleReasoning:  baseStyle.Foreground(muted),
			presentation.RoleSuccess:    baseStyle.Foreground(green),
			presentation.RoleWarning:    baseStyle.Foreground(amber).Bold(true),
			presentation.RoleFailure:    baseStyle.Foreground(red).Bold(true),
			presentation.RoleMuted:      baseStyle.Foreground(muted),
			presentation.RoleActive:     baseStyle.Foreground(amber),
			presentation.RoleSelected:   baseStyle.Foreground(background).Background(cyan).Bold(true),
		},
		Canvas:        baseStyle,
		Text:          baseStyle.Foreground(text),
		Title:         baseStyle.Foreground(cyan).Bold(true).Padding(0, 1),
		Divider:       baseStyle.Foreground(line),
		Panel:         baseStyle.Padding(1),
		FocusedPanel:  baseStyle.Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("#2B2B2B")),
		Composer:      baseStyle,
		MarkdownStyle: markdownStyle,
	}
}

func colorValue(value string) *string { return &value }

func (theme Theme) Role(role presentation.SemanticRole) lipgloss.Style {
	if theme.Roles == nil {
		theme = defaultTheme
	}

	if style, ok := theme.Roles[role]; ok {
		return style
	}

	return theme.Text
}

func (m model) activeTheme() Theme {
	if m.theme.Roles == nil {
		return defaultTheme
	}

	return m.theme
}

func (m model) style(role presentation.SemanticRole) lipgloss.Style {
	return m.activeTheme().Role(role)
}
