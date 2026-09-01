package setup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
)

// iamManagedPolicyVersionLimit is IAM's fixed cap on versions per
// customer-managed policy. It is not an adjustable quota, so the provisioner
// prunes to stay under it rather than asking the operator to.
const iamManagedPolicyVersionLimit = 5

// BackupUserPolicyNameFor names the customer-managed policy that grants
// BackupUserName access to one bucket. One policy per bucket is what lets
// buckets accumulate on the user (IAM attaches up to ten managed policies to
// a user) instead of each wizard run replacing the previous run's grant, as
// the single inline policy used to. Bucket names are [a-z0-9.-] and at most
// 63 characters, so the result is always a valid IAM policy name.
func BackupUserPolicyNameFor(bucket string) string {
	return BackupUserPolicyName + "-" + bucket
}

// policyARNFromUserARN derives the ARN of a customer-managed policy in the
// user's own account from the user's ARN — partition included, so the
// derivation holds in GovCloud and China, where a hard-coded "arn:aws" would
// name a policy that cannot exist. It is needed because CreatePolicy's
// EntityAlreadyExists carries no ARN for the policy that already exists.
func policyARNFromUserARN(userARN, policyName string) (string, error) {
	parts := strings.SplitN(userARN, ":", 6)
	if len(parts) != 6 || parts[0] != "arn" || parts[2] != "iam" || parts[4] == "" {
		return "", fmt.Errorf("unexpected IAM user ARN %q", userARN)
	}
	return "arn:" + parts[1] + ":iam::" + parts[4] + ":policy/" + policyName, nil
}

// parsePolicyDocument decodes a document as IAM returns it — URL-encoded per
// RFC 3986 — into Sentra's policy shape. A document not in that shape (a
// hand-written policy with a bare-string Action, say) fails to parse, and
// callers treat that as "nothing to merge with", never as a fatal error.
func parsePolicyDocument(encoded string) (IAMPolicyDocument, error) {
	raw, err := url.PathUnescape(encoded)
	if err != nil {
		return IAMPolicyDocument{}, fmt.Errorf("decode policy document: %w", err)
	}
	var doc IAMPolicyDocument
	if err := json.Unmarshal([]byte(raw), &doc); err != nil {
		return IAMPolicyDocument{}, fmt.Errorf("parse policy document: %w", err)
	}
	return doc, nil
}

// policyDocumentEqual compares two documents by their canonical JSON, which
// is also exactly what IAM stores — so "equal" means a rerun would write the
// bytes the policy already holds.
func policyDocumentEqual(a, b IAMPolicyDocument) bool {
	ja, errA := json.Marshal(a)
	jb, errB := json.Marshal(b)
	return errA == nil && errB == nil && string(ja) == string(jb)
}

// mergePolicyResources returns desired with, per statement, the union of its
// resources and those of existing's statement of the same Sid. Actions,
// effects, and conditions come from desired alone: the canonical policy is
// authoritative for WHAT is allowed and the stored one only for WHERE, so a
// second prefix in the same bucket accumulates, while a canonical policy that
// gained an action since the stored version still wins. Resources are sorted
// so a rerun reproduces the stored document byte for byte and is recognised
// as a reuse instead of burning one of the five versions. A nil existing
// normalizes desired the same way, which is what makes the first write and
// every later comparison agree on ordering.
func mergePolicyResources(desired IAMPolicyDocument, existing *IAMPolicyDocument) IAMPolicyDocument {
	out := desired
	out.Statement = make([]IAMPolicyStatement, len(desired.Statement))
	for i, s := range desired.Statement {
		merged := slices.Clone(s.Resource)
		if existing != nil {
			for _, e := range existing.Statement {
				if e.Sid == s.Sid {
					merged = append(merged, e.Resource...)
				}
			}
		}
		slices.Sort(merged)
		s.Resource = slices.Compact(merged)
		s.Action = slices.Clone(s.Action)
		out.Statement[i] = s
	}
	return out
}

// policyCovers reports whether super grants everything sub grants: every
// statement of sub has a same-Sid statement in super with the same effect,
// no condition on either side, and actions and resources that are subsets.
// This is set containment over Sentra-shaped documents, not IAM evaluation —
// a wildcard in super does not cover a literal in sub. That conservatism is
// the point: the caller deletes sub on a true answer.
func policyCovers(super, sub IAMPolicyDocument) bool {
	for _, s := range sub.Statement {
		i := slices.IndexFunc(super.Statement, func(c IAMPolicyStatement) bool { return c.Sid == s.Sid })
		if i < 0 {
			return false
		}
		c := super.Statement[i]
		if c.Effect != s.Effect || c.Condition != nil || s.Condition != nil {
			return false
		}
		if !subset(s.Action, c.Action) || !subset(s.Resource, c.Resource) {
			return false
		}
	}
	return true
}

func subset(sub, super []string) bool {
	for _, x := range sub {
		if !slices.Contains(super, x) {
			return false
		}
	}
	return true
}

// oldestNonDefaultVersion picks the version to delete when a policy is at
// the version limit: the oldest by creation date that is not the default,
// which IAM refuses to delete. Empty when every version is the default — a
// one-version policy is never at the limit, so callers do not see that.
func oldestNonDefaultVersion(versions []iamtypes.PolicyVersion) string {
	var (
		id     string
		oldest time.Time
	)
	for _, v := range versions {
		if v.IsDefaultVersion {
			continue
		}
		created := aws.ToTime(v.CreateDate)
		if id == "" || created.Before(oldest) {
			id, oldest = aws.ToString(v.VersionId), created
		}
	}
	return id
}

// backupPolicyOutcome is the non-secret result of ensureBackupPolicy.
type backupPolicyOutcome struct {
	arn     string
	created bool              // CreatePolicy succeeded
	updated bool              // an existing policy received a new default version
	doc     IAMPolicyDocument // what the default version now holds
}

// ensureBackupPolicy makes the per-bucket managed policy carry the canonical
// grant for bucket/prefix: it creates the policy or — when it already exists
// — merges the stored resources with ours and writes a new default version
// only if that changed anything. The merge is what keeps a sibling prefix's
// grant alive; the reuse check is what keeps reruns from burning the
// five-version limit; pruning the oldest non-default version first is what
// keeps that limit from ever failing a run.
func ensureBackupPolicy(ctx context.Context, client iamAPI, userARN, bucket, prefix string) (backupPolicyOutcome, error) {
	name := BackupUserPolicyNameFor(bucket)
	arn, err := policyARNFromUserARN(userARN, name)
	if err != nil {
		return backupPolicyOutcome{}, &BackupUserError{Step: "iam:CreatePolicy", Err: err}
	}
	out := backupPolicyOutcome{arn: arn, doc: mergePolicyResources(BuildIAMPolicy(bucket, prefix), nil)}
	desired, err := json.Marshal(out.doc)
	if err != nil {
		return out, &BackupUserError{Step: "iam:CreatePolicy", Err: fmt.Errorf("encode policy: %w", err)}
	}
	_, err = client.CreatePolicy(ctx, &iam.CreatePolicyInput{
		PolicyName:     aws.String(name),
		PolicyDocument: aws.String(string(desired)),
		Description:    aws.String("Sentra least-privilege backup access to bucket " + bucket + ". Created by sentra setup."),
	})
	switch {
	case err == nil:
		out.created = true
		return out, nil
	case isIAMEntityExists(err):
		// Fall through to reconcile with the existing policy.
	default:
		return out, classifyIAMError("iam:CreatePolicy", err)
	}

	list, err := client.ListPolicyVersions(ctx, &iam.ListPolicyVersionsInput{PolicyArn: aws.String(arn)})
	if err != nil {
		return out, classifyIAMError("iam:ListPolicyVersions", err)
	}
	var versions []iamtypes.PolicyVersion
	if list != nil {
		versions = list.Versions
	}
	var existing *IAMPolicyDocument
	for _, v := range versions {
		if !v.IsDefaultVersion {
			continue
		}
		got, err := client.GetPolicyVersion(ctx, &iam.GetPolicyVersionInput{PolicyArn: aws.String(arn), VersionId: v.VersionId})
		if err != nil {
			return out, classifyIAMError("iam:GetPolicyVersion", err)
		}
		if got != nil && got.PolicyVersion != nil && got.PolicyVersion.Document != nil {
			// A document that is not Sentra's shape cannot be merged; the
			// canonical document replaces it in a new version.
			if doc, err := parsePolicyDocument(*got.PolicyVersion.Document); err == nil {
				existing = &doc
			}
		}
		break
	}
	out.doc = mergePolicyResources(out.doc, existing)
	if existing != nil && policyDocumentEqual(out.doc, *existing) {
		return out, nil
	}
	if len(versions) >= iamManagedPolicyVersionLimit {
		if victim := oldestNonDefaultVersion(versions); victim != "" {
			if _, err := client.DeletePolicyVersion(ctx, &iam.DeletePolicyVersionInput{
				PolicyArn: aws.String(arn), VersionId: aws.String(victim),
			}); err != nil {
				return out, classifyIAMError("iam:DeletePolicyVersion", err)
			}
		}
	}
	merged, err := json.Marshal(out.doc)
	if err != nil {
		return out, &BackupUserError{Step: "iam:CreatePolicyVersion", Err: fmt.Errorf("encode policy: %w", err)}
	}
	if _, err := client.CreatePolicyVersion(ctx, &iam.CreatePolicyVersionInput{
		PolicyArn:      aws.String(arn),
		PolicyDocument: aws.String(string(merged)),
		SetAsDefault:   true,
	}); err != nil {
		return out, classifyIAMError("iam:CreatePolicyVersion", err)
	}
	out.updated = true
	return out, nil
}

// removeLegacyInlinePolicy migrates installs from before per-bucket managed
// policies, which carried one inline policy named BackupUserPolicyName on
// the user. It runs only after the managed policy is attached (the caller's
// ordering) and deletes only when that policy covers every grant the inline
// one made: an inline policy for a DIFFERENT bucket is an older install's
// only grant for it and stays. Best-effort by design — a leftover inline
// policy is inert residue, and failing the run over it would keep setup on
// expiring session credentials for no gain. Reports whether it was removed.
func removeLegacyInlinePolicy(ctx context.Context, client iamAPI, managed IAMPolicyDocument) bool {
	got, err := client.GetUserPolicy(ctx, &iam.GetUserPolicyInput{
		UserName: aws.String(BackupUserName), PolicyName: aws.String(BackupUserPolicyName),
	})
	if err != nil || got == nil || got.PolicyDocument == nil {
		return false
	}
	inline, err := parsePolicyDocument(*got.PolicyDocument)
	if err != nil || !policyCovers(managed, inline) {
		return false
	}
	_, err = client.DeleteUserPolicy(ctx, &iam.DeleteUserPolicyInput{
		UserName: aws.String(BackupUserName), PolicyName: aws.String(BackupUserPolicyName),
	})
	return err == nil
}

func isIAMEntityExists(err error) bool {
	var exists *iamtypes.EntityAlreadyExistsException
	return errors.As(err, &exists)
}
