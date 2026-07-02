package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/markgustetic/sentra/internal/repo"
	"github.com/markgustetic/sentra/internal/ui"
)

// OperationsData is the read-only health snapshot rendered by the
// operations view.
type OperationsData struct {
	Report repo.CheckReport
	Err    error
}

// Operations is the TUI health and maintenance view.
type Operations struct {
	deps Deps
	data OperationsData
}

// NewOperations returns a hydrated operations view.
func NewOperations(deps Deps) Operations {
	o := Operations{deps: deps}
	o.data = hydrateOperationsData(deps)
	return o
}

// Title names the view in the sidebar, palette, and title bar.
func (Operations) Title() string { return "Operations" }

// ShortHelp lists the view-specific keys for the status bar.
func (Operations) ShortHelp() []key.Binding { return nil }

// SetData replaces the model's data. Tests use this to inject canned
// check reports.
func (o Operations) SetData(data OperationsData) Operations {
	o.data = data
	return o
}

func hydrateOperationsData(deps Deps) OperationsData {
	if deps.Repo == nil {
		return OperationsData{}
	}
	ctx, cancel := context.WithTimeout(ctxOrBackground(deps.Ctx), 5*time.Second)
	defer cancel()
	report, err := deps.Repo.Check(ctx, repo.CheckOptions{})
	return OperationsData{Report: report, Err: err}
}

func (Operations) Init() tea.Cmd { return nil }

func (o Operations) Update(_ tea.Msg) (tea.Model, tea.Cmd) {
	return o, nil
}

func (o Operations) View() string {
	statusPanel := o.renderStatusPanel()
	blobPanel := o.renderBlobPanel()
	issuePanel := o.renderIssuePanel()

	row1 := lipgloss.JoinHorizontal(lipgloss.Top, statusPanel, blobPanel)
	return lipgloss.JoinVertical(lipgloss.Left, row1, issuePanel) + "\n"
}

func (o Operations) renderStatusPanel() string {
	title := ui.Subtle.Render("operations")
	if o.data.Err != nil {
		body := fmt.Sprintf("%s\n%s\n%s",
			title,
			ui.Warn.Render("check unavailable"),
			ui.Muted.Render(o.data.Err.Error()),
		)
		return ui.Panel.Render(body)
	}

	report := o.data.Report
	status := ui.Success.Render("healthy")
	if !report.Healthy() {
		status = ui.Warn.Render("failed")
	}
	body := fmt.Sprintf("%s\n%s\n%d snapshots\n%d files",
		title,
		status,
		report.Snapshots,
		report.Files,
	)
	return ui.Panel.Render(body)
}

func (o Operations) renderBlobPanel() string {
	report := o.data.Report
	body := fmt.Sprintf("%s\n%d referenced blobs\n%d data blobs\n%s orphan bytes",
		ui.Subtle.Render("storage"),
		report.ReferencedBlobs,
		report.DataBlobs,
		ui.FormatBytes(report.OrphanBytes),
	)
	return ui.Panel.Render(body)
}

func (o Operations) renderIssuePanel() string {
	report := o.data.Report
	lines := []string{ui.Subtle.Render("issues")}
	if o.data.Err != nil {
		lines = append(lines, ui.Warn.Render("run sentra check for details"))
		return ui.Panel.Render(strings.Join(lines, "\n"))
	}
	if len(report.ManifestIssues) == 0 &&
		len(report.MissingBlobs) == 0 &&
		len(report.OrphanBlobs) == 0 &&
		(report.Lock == nil || (!report.Lock.Stale && !report.Lock.Unreadable)) {
		lines = append(lines, ui.Success.Render("no integrity issues found"))
		return ui.Panel.Render(strings.Join(lines, "\n"))
	}

	if len(report.ManifestIssues) > 0 {
		lines = append(lines, ui.Warn.Render(fmt.Sprintf("%d manifest issues", len(report.ManifestIssues))))
	}
	if len(report.MissingBlobs) > 0 {
		lines = append(lines, ui.Warn.Render(fmt.Sprintf("%d missing blobs", len(report.MissingBlobs))))
	}
	if len(report.OrphanBlobs) > 0 {
		lines = append(lines, ui.Subtle.Render(fmt.Sprintf("%d orphan blobs", len(report.OrphanBlobs))))
	}
	if report.Lock != nil {
		switch {
		case report.Lock.Unreadable:
			lines = append(lines, ui.Warn.Render("unreadable lock"))
		case report.Lock.Stale:
			lines = append(lines, ui.Warn.Render("stale lock: "+operationDash(report.Lock.Operation)))
		default:
			lines = append(lines, ui.Subtle.Render("active lock: "+operationDash(report.Lock.Operation)))
		}
	}
	return ui.Panel.Render(strings.Join(lines, "\n"))
}

func operationDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
