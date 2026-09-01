package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/spf13/cobra"
	"github.com/joecattt/thaw/pkg/models"
	"github.com/joecattt/thaw/internal/snapshot"
)

var (
	baseStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#f0f8fc")).Background(lipgloss.Color("#0a2435")).Padding(1, 2)
	titleStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#ffffff")).Bold(true).Border(lipgloss.NormalBorder()).BorderForeground(lipgloss.Color("#2a6878")).Padding(0, 1)
	activeStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#34d399")).Bold(true)
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#5a9ab2"))
)

type tuiModel struct {
	projects []string
	cursor   int
	snap     *models.Snapshot
	selected string
}

func (m tuiModel) Init() tea.Cmd { return nil }

func (m tuiModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q", "esc":
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.projects)-1 {
				m.cursor++
			}
		case "enter":
			if len(m.projects) > 0 {
				m.selected = m.projects[m.cursor]
			}
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m tuiModel) View() string {
	s := titleStyle.Render("thaw - ICE Terminal Interface") + "\n\n"
	
	if len(m.projects) == 0 {
		return s + "No projects found.\n"
	}
	
	for i, proj := range m.projects {
		cursor := "  "
		style := dimStyle
		if m.cursor == i {
			cursor = "❯ "
			style = activeStyle
		}
		s += style.Render(fmt.Sprintf("%s%s", cursor, proj)) + "\n"
	}
	
	s += dimStyle.Render("\n[↑/↓: navigate] [enter: select] [q: quit]")
	return baseStyle.Render(s)
}

func tuiCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tui",
		Short: "Open the ICE terminal interface",
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := snapshot.Open()
			if err != nil { return err }
			defer store.Close()

			snap, err := store.Latest()
			if err != nil || snap == nil {
				fmt.Println("Nothing to thaw. Run `thaw freeze` first.")
				return nil
			}

			projs := make([]string, 0)
			seen := make(map[string]bool)
			for _, s := range snap.Sessions {
				if !seen[s.CWD] {
					projs = append(projs, s.CWD)
					seen[s.CWD] = true
				}
			}

			p := tea.NewProgram(tuiModel{projects: projs, snap: snap}, tea.WithAltScreen())
			finalModel, err := p.Run()
			if err != nil {
				fmt.Println("Error running TUI:", err)
				os.Exit(1)
			}
			
			if tm, ok := finalModel.(tuiModel); ok && tm.selected != "" {
				fmt.Printf("\n✨ Selected project: %s\n", tm.selected)
				fmt.Printf("To resume work, run:\n  cd %s\n  thaw recall\n\n", tm.selected)
			}
			return nil
		},
	}
}
