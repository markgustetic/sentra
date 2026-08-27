package tui

import (
	"errors"
	"strings"
	"testing"
)

// The raw chain the connect gate showed for an expired AWS browser-login
// session — the failure that motivated humanizing known errors.
const expiredLoginChain = `open repo: repo: get config: blobstore/s3: get "config": operation error S3: GetObject, get identity: get credentials: failed to refresh cached credentials, create oauth2 token: login session has expired, please reauthenticate`

// humanizeErr's rule: a recognized cause leads with plain words and a fix,
// and the raw chain stays below — the summary is for the operator, the
// chain is for the bug report. (Tests run under the Ascii profile, so
// styled fragments compare as bare text.)
func TestHumanizeErr_KnownCauseLeadsAndKeepsDetail(t *testing.T) {
	got := humanizeErr(errors.New(expiredLoginChain))
	if !strings.Contains(got, "your AWS login session has expired") {
		t.Errorf("missing plain-words summary:\n%s", got)
	}
	if !strings.Contains(got, "sign in") {
		t.Errorf("missing fix line:\n%s", got)
	}
	if !strings.Contains(got, expiredLoginChain) {
		t.Errorf("raw chain dropped — the detail must survive for bug reports:\n%s", got)
	}
	if !strings.HasPrefix(got, "your AWS login session has expired") {
		t.Errorf("summary must come first:\n%s", got)
	}
}

// An unrecognized error renders exactly as before: the raw chain, nothing
// invented around it.
func TestHumanizeErr_UnknownRendersRawOnly(t *testing.T) {
	if got := humanizeErr(errors.New("something novel broke")); got != "something novel broke" {
		t.Errorf("humanizeErr(unknown) = %q, want the raw text unchanged", got)
	}
}

// The connect gate is where the motivating error appeared: its frame must
// lead with the plain-words reading and keep the raw chain visible.
func TestConnect_ExplainsKnownOpenError(t *testing.T) {
	deps := connectDeps(nil)
	deps.ConnectError = errors.New(expiredLoginChain)
	view := NewConnectView(deps).View()
	if !strings.Contains(view, "your AWS login session has expired") {
		t.Errorf("connect view missing plain-words summary:\n%s", view)
	}
	if !strings.Contains(view, expiredLoginChain) {
		t.Errorf("connect view dropped the raw chain:\n%s", view)
	}
}

// The unlock gate opens the repo too, so the same credential failures can
// surface there; wrong-passphrase keeps its dedicated message.
func TestUnlockErrMessage_ExplainsKnownCause(t *testing.T) {
	got := unlockErrMessage(errors.New(expiredLoginChain))
	if !strings.Contains(got, "your AWS login session has expired") {
		t.Errorf("unlock message missing plain-words summary:\n%s", got)
	}
	if !strings.Contains(got, expiredLoginChain) {
		t.Errorf("unlock message dropped the raw chain:\n%s", got)
	}
}

// Operation failures funnel through ErrorModal or the per-view result
// panes, all of which render via humanizeErr; the modal is the shared
// surface, so pin it. Single-word asserts: ModalBox wraps at ~64 columns
// and may insert newlines inside longer phrases.
func TestErrorModal_ExplainsKnownError(t *testing.T) {
	m := NewErrorModal(errors.New(expiredLoginChain), "", 100, 40)
	view := m.View()
	if !strings.Contains(view, "AWS") {
		t.Errorf("error modal missing plain-words summary:\n%s", view)
	}
	if !strings.Contains(view, "GetObject,") {
		t.Errorf("error modal dropped the raw chain:\n%s", view)
	}
}
