// Package present provides restrained terminal styling for human output.
package present

import (
	"io"
	"os"

	"github.com/charmbracelet/lipgloss"
)

type Renderer struct {
	color bool
}

func New(mode string, out io.Writer) Renderer {
	color := mode == "always" || (mode == "auto" && os.Getenv("NO_COLOR") == "" && isTerminal(out))
	return Renderer{color: color}
}

func (r Renderer) Heading(value string) string {
	if !r.color {
		return value
	}
	return lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("12")).Render(value)
}

func (r Renderer) Success(value string) string {
	if !r.color {
		return value
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Render(value)
}

func (r Renderer) Warning(value string) string {
	if !r.color {
		return value
	}
	return lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Render(value)
}

func isTerminal(writer io.Writer) bool {
	file, ok := writer.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}
