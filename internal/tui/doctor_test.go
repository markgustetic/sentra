package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/markgustetic/sentra/internal/config"
	"github.com/markgustetic/sentra/internal/diag"
)

// runDoctorCollect drives the DoctorView from idle through Enter and
// returns the terminal doctorDoneMsg produced by the run cmd. It bypasses
// the spinner tick (the run cmd is the last element of the Enter batch).
func runDoctorCollect(t *testing.T, v DoctorView) doctorDoneMsg {
	t.Helper()
	m, cmd := v.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.(DoctorView)
	if v.stage != doctorRunning {
		t.Fatalf("stage after Enter = %v, want doctorRunning", v.stage)
	}
	msgs := execCmds(t, cmd)
	for _, msg := range msgs {
		if done, ok := msg.(doctorDoneMsg); ok {
			return done
		}
	}
	t.Fatal("Enter batch produced no doctorDoneMsg")
	return doctorDoneMsg{}
}

func TestDoctorView_NilConfigFailsConfigCheck(t *testing.T) {
	r := newFlowRepo(t)
	v := NewDoctorView(Deps{Repo: r, Config: nil})
	done := runDoctorCollect(t, v)
	found := false
	for _, row := range done.rows {
		if row.status == doctorFail && strings.Contains(row.label, "onfig") {
			found = true
		}
	}
	if !found {
		t.Fatalf("nil config should yield a failing config row: %+v", done.rows)
	}
	if done.healthy {
		t.Fatal("doctor should not be healthy when config check fails")
	}
}

func TestDoctorView_RepoHealthyRow(t *testing.T) {
	r := newFlowRepo(t)
	cfg := config.Defaults()
	cfg.Repo.S3.Bucket = "sentra-prod"
	cfg.Repo.S3.EndpointURL = "http://localhost:9000" // skip AWS legs
	v := NewDoctorView(Deps{Repo: r, Config: &cfg})
	done := runDoctorCollect(t, v)
	sawRepoOK := false
	sawBucketOK := false
	for _, row := range done.rows {
		if row.status == doctorOK && strings.Contains(row.label, "epository") {
			sawRepoOK = true
		}
		if row.status == doctorOK && strings.Contains(row.label, "ucket name") {
			sawBucketOK = true
		}
	}
	if !sawRepoOK {
		t.Fatalf("expected a healthy repository row: %+v", done.rows)
	}
	if !sawBucketOK {
		t.Fatalf("expected a valid bucket-name row: %+v", done.rows)
	}
	if !done.healthy {
		t.Fatalf("expected healthy overall, got issues: %+v", done.rows)
	}
}

func TestDoctorView_InvalidBucketFails(t *testing.T) {
	r := newFlowRepo(t)
	cfg := config.Defaults()
	cfg.Repo.S3.Bucket = "Bad_Bucket"
	cfg.Repo.S3.EndpointURL = "http://localhost:9000"
	v := NewDoctorView(Deps{Repo: r, Config: &cfg})
	done := runDoctorCollect(t, v)
	sawBucketFail := false
	for _, row := range done.rows {
		if row.status == doctorFail && strings.Contains(row.detail, "lowercase") {
			sawBucketFail = true
		}
	}
	if !sawBucketFail {
		t.Fatalf("invalid bucket should produce a failing row explaining naming: %+v", done.rows)
	}
	if done.healthy {
		t.Fatal("doctor should not be healthy with an invalid bucket name")
	}
}

func TestDoctorView_RendersRowsAfterDone(t *testing.T) {
	r := newFlowRepo(t)
	cfg := config.Defaults()
	cfg.Repo.S3.Bucket = "sentra-prod"
	cfg.Repo.S3.EndpointURL = "http://localhost:9000"
	v := NewDoctorView(Deps{Repo: r, Config: &cfg})
	done := runDoctorCollect(t, v)
	m, _ := v.Update(done)
	v = m.(DoctorView)
	out := v.View()
	if !strings.Contains(out, "healthy") {
		t.Fatalf("done view should show the healthy summary:\n%s", out)
	}
}

// TestDoctorView_UsesDiagAlias pins the DoctorView against the same
// diag.AWSReport shape the cli path uses.
func TestDoctorView_UsesDiagAlias(t *testing.T) {
	_ = diag.AWSReport{BucketAccessible: true}
}
