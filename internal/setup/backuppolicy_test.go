package setup

import (
	"net/url"
	"testing"
)

func TestBackupUserPolicyNameFor(t *testing.T) {
	if got := BackupUserPolicyNameFor("my-backups.example"); got != "sentra-s3-backup-my-backups.example" {
		t.Fatalf("name = %q", got)
	}
}

// The policy ARN is derived from the user ARN, not formatted from a hard-coded
// "arn:aws" partition, so GovCloud and China accounts get a valid ARN too.
func TestPolicyARNFromUserARN(t *testing.T) {
	tests := []struct {
		user, want string
		wantErr    bool
	}{
		{"arn:aws:iam::123456789012:user/sentra-backup", "arn:aws:iam::123456789012:policy/p", false},
		{"arn:aws-us-gov:iam::123456789012:user/sentra-backup", "arn:aws-us-gov:iam::123456789012:policy/p", false},
		{"arn:aws:iam::123456789012:user/team/sentra-backup", "arn:aws:iam::123456789012:policy/p", false},
		{"", "", true},
		{"arn:aws:s3:::bucket", "", true},
		{"arn:aws:iam:::user/x", "", true},
	}
	for _, tc := range tests {
		got, err := policyARNFromUserARN(tc.user, "p")
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("policyARNFromUserARN(%q) = %q, %v; want %q, err=%v", tc.user, got, err, tc.want, tc.wantErr)
		}
	}
}

func TestParsePolicyDocument_DecodesURLEncoding(t *testing.T) {
	want := BuildIAMPolicy("b", "p/")
	encoded := url.PathEscape(mustMarshalPolicy(t, want))
	got, err := parsePolicyDocument(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if !policyDocumentEqual(got, want) {
		t.Fatalf("round trip lost information:\n%+v\nwant\n%+v", got, want)
	}
	if _, err := parsePolicyDocument("%zz"); err == nil {
		t.Fatal("bad escape must error")
	}
	if _, err := parsePolicyDocument("not json"); err == nil {
		t.Fatal("non-JSON must error")
	}
}

// Merging is per statement, by Sid: the existing policy's resources are
// unioned into ours so a second prefix in the same bucket ADDS a grant
// instead of replacing the first one. Output order is sorted so a rerun
// reproduces the stored document byte for byte and is recognised as a reuse.
func TestMergePolicyResources_UnionsBySidSorted(t *testing.T) {
	desired := BuildIAMPolicy("b", "laptop/")
	existing := BuildIAMPolicy("b", "desktop/")
	merged := mergePolicyResources(desired, &existing)

	objects := statementBySid(t, merged, "SentraRepositoryObjects")
	want := []string{"arn:aws:s3:::b/desktop/*", "arn:aws:s3:::b/laptop/*"}
	if len(objects.Resource) != 2 || objects.Resource[0] != want[0] || objects.Resource[1] != want[1] {
		t.Fatalf("objects resources = %v, want %v", objects.Resource, want)
	}
	// Bucket-level statements name the same ARN on both sides: no duplicate.
	if list := statementBySid(t, merged, "SentraListBucket"); len(list.Resource) != 1 {
		t.Fatalf("bucket statement duplicated: %v", list.Resource)
	}
	// Actions come from the desired (canonical) side only, so a canonical
	// policy that gained an action since the stored version wins.
	if len(objects.Action) != len(desired.Statement[2].Action) {
		t.Fatalf("actions = %v", objects.Action)
	}
	// Merging must not alias the input's slices.
	if &merged.Statement[0].Resource[0] == &desired.Statement[0].Resource[0] {
		t.Fatal("merge aliased the desired document")
	}
}

func TestMergePolicyResources_NilExistingNormalizes(t *testing.T) {
	desired := BuildIAMPolicy("b", "p/")
	if got := mergePolicyResources(desired, nil); !policyDocumentEqual(got, desired) {
		t.Fatalf("nil existing must yield the desired document, got %+v", got)
	}
}

// policyCovers is the rule the legacy-cleanup step relies on: the inline
// policy may be deleted only when every grant it made is also made by the
// managed policy the user now holds.
func TestPolicyCovers(t *testing.T) {
	managed := mergePolicyResources(BuildIAMPolicy("b", "laptop/"), ptr(BuildIAMPolicy("b", "desktop/")))
	tests := []struct {
		name string
		sub  IAMPolicyDocument
		want bool
	}{
		{"same repo", BuildIAMPolicy("b", "laptop/"), true},
		{"sibling prefix merged in", BuildIAMPolicy("b", "desktop/"), true},
		{"other bucket", BuildIAMPolicy("other", "laptop/"), false},
		{"prefix not granted", BuildIAMPolicy("b", "phone/"), false},
		{"whole bucket is wider than any prefix", BuildIAMPolicy("b", ""), false},
		{"extra action", withAction(BuildIAMPolicy("b", "laptop/"), "s3:DeleteBucket"), false},
		{"deny statement", withEffect(BuildIAMPolicy("b", "laptop/"), "Deny"), false},
		{"conditioned statement", withCondition(BuildIAMPolicy("b", "laptop/")), false},
		{"empty policy grants nothing", IAMPolicyDocument{Version: "2012-10-17"}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := policyCovers(managed, tc.sub); got != tc.want {
				t.Fatalf("policyCovers = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestOldestNonDefaultVersion(t *testing.T) {
	// v3 is the default and must never be chosen; v1 is the oldest by date
	// even though the list is unordered.
	versions := fakeVersions([]fakePolicyVersion{
		{id: "v2", created: 2},
		{id: "v3", created: 3, isDefault: true},
		{id: "v1", created: 1},
	})
	if got := oldestNonDefaultVersion(versions); got != "v1" {
		t.Fatalf("oldest = %q, want v1", got)
	}
	if got := oldestNonDefaultVersion(fakeVersions([]fakePolicyVersion{{id: "v1", isDefault: true}})); got != "" {
		t.Fatalf("only the default left: got %q, want empty", got)
	}
}

func statementBySid(t *testing.T, doc IAMPolicyDocument, sid string) IAMPolicyStatement {
	t.Helper()
	for _, s := range doc.Statement {
		if s.Sid == sid {
			return s
		}
	}
	t.Fatalf("no statement %q in %+v", sid, doc)
	return IAMPolicyStatement{}
}

func ptr[T any](v T) *T { return &v }

func withAction(doc IAMPolicyDocument, action string) IAMPolicyDocument {
	doc.Statement[2].Action = append(append([]string(nil), doc.Statement[2].Action...), action)
	return doc
}

func withEffect(doc IAMPolicyDocument, effect string) IAMPolicyDocument {
	doc.Statement[2].Effect = effect
	return doc
}

func withCondition(doc IAMPolicyDocument) IAMPolicyDocument {
	doc.Statement[2].Condition = map[string]any{"Bool": map[string]any{"aws:SecureTransport": "true"}}
	return doc
}
