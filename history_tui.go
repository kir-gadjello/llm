package main

import (
	"fmt"
	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/kir-gadjello/llm/history"
)

type historyItem struct {
	summary history.SessionSummary
}

func (h historyItem) Title() string {
	return fmt.Sprintf("%s (%s)", h.summary.Timestamp.Format("01/02 15:04"), h.summary.Model)
}
func (h historyItem) Description() string { return h.summary.Summary }
func (h historyItem) FilterValue() string { return h.summary.Summary + " " + h.summary.Model }

type historyModel struct {
	list     list.Model
	selected *history.SessionSummary
	quitting bool
}

func newHistoryModel(sessions []history.SessionSummary) historyModel {
	items := make([]list.Item, len(sessions))
	for i, s := range sessions {
		items[i] = historyItem{summary: s}
	}

	l := list.New(items, list.NewDefaultDelegate(), 0, 0)
	l.Title = "Recent Chats"
	l.Styles.Title = lipgloss.NewStyle().
		Foreground(lipgloss.Color("#FFF")).
		Background(lipgloss.Color("#7D56F4")).
		Padding(0, 1)

	return historyModel{list: l}
}

func (m historyModel) Init() tea.Cmd {
	return nil
}

func (m historyModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" || msg.String() == "q" {
			m.quitting = true
			return m, tea.Quit
		}
		if msg.String() == "enter" {
			if i, ok := m.list.SelectedItem().(historyItem); ok {
				m.selected = &i.summary
				return m, tea.Quit
			}
		}
	case tea.WindowSizeMsg:
		h, v := lipgloss.NewStyle().Margin(1, 2).GetFrameSize()
		m.list.SetSize(msg.Width-h, msg.Height-v)
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m historyModel) View() string {
	if m.quitting {
		return ""
	}
	return lipgloss.NewStyle().Margin(1, 2).Render(m.list.View())
}
