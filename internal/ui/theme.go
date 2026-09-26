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
	Roles         map[presentation.SemanticRole]lipgloss.Style
	Canvas        lipgloss.Style
	Text          lipgloss.Style
	Title         lipgloss.Style
	Divider       lipgloss.Style
	ApprovalPanel lipgloss.Style
	MarkdownStyle ansi.StyleConfig
}

var defaultTheme = NewDefaultTheme()

func NewDefaultTheme() Theme {
	const (
		background = lipgloss.Color("#10151E")
		text       = lipgloss.Color("#D9E1EA")
		muted      = lipgloss.Color("#788697")
		cyan       = lipgloss.Color("#75D9E9")
		green      = lipgloss.Color("#9AD7A5")
		amber      = lipgloss.Color("#E5BE80")
		violet     = lipgloss.Color("#B5A3EB")
		red        = lipgloss.Color("#EA8D97")
		line       = lipgloss.Color("#465264")
	)
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
		Roles: map[presentation.SemanticRole]lipgloss.Style{
			presentation.RoleNavigation: lipgloss.NewStyle().Foreground(cyan),
			presentation.RoleRead:       lipgloss.NewStyle().Foreground(cyan),
			presentation.RoleMutation:   lipgloss.NewStyle().Foreground(violet),
			presentation.RoleExecution:  lipgloss.NewStyle().Foreground(amber),
			presentation.RoleReasoning:  lipgloss.NewStyle().Foreground(muted),
			presentation.RoleSuccess:    lipgloss.NewStyle().Foreground(green),
			presentation.RoleWarning:    lipgloss.NewStyle().Foreground(amber).Bold(true),
			presentation.RoleFailure:    lipgloss.NewStyle().Foreground(red).Bold(true),
			presentation.RoleMuted:      lipgloss.NewStyle().Foreground(muted),
			presentation.RoleActive:     lipgloss.NewStyle().Foreground(amber),
			presentation.RoleSelected:   lipgloss.NewStyle().Foreground(background).Background(cyan).Bold(true),
		},
		Canvas:        lipgloss.NewStyle(),
		Text:          lipgloss.NewStyle().Foreground(text),
		Title:         lipgloss.NewStyle().Foreground(cyan).Bold(true),
		Divider:       lipgloss.NewStyle().Foreground(line),
		ApprovalPanel: lipgloss.NewStyle().Border(lipgloss.NormalBorder(), false, false, false, true).BorderForeground(amber).PaddingLeft(1),
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
