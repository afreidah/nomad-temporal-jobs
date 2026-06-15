// -------------------------------------------------------------------------------
// Cert Acquirer Workflow - Wildcard Issuance then Vault Publish
//
// Author: Alex Freidah
//
// Orchestrates the weekly wildcard renewal: issue the certificate via ACME
// DNS-01, then publish it to the Vault path Traefik reads. The two steps are
// separate activities with different retry policies. Issuance gets few
// attempts with long backoff because DNS-01 propagation is slow and Let's
// Encrypt rate-limits duplicate issuance; publish gets fast retries because
// it only moves an already-issued cert between Vault paths.
// -------------------------------------------------------------------------------

package workflows

import (
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"munchbox/temporal-workers/certacquirer/activities"
)

// a is a nil receiver used only for activity method-expression references;
// Temporal resolves activities by name against the registered struct instance.
var a *activities.Activities

// -------------------------------------------------------------------------
// RETRY POLICIES
// -------------------------------------------------------------------------

// retryIssue keeps attempts low and backoff long: DNS-01 propagation is slow
// and Let's Encrypt rate-limits repeated issuance.
var retryIssue = &temporal.RetryPolicy{
	InitialInterval:    time.Minute,
	BackoffCoefficient: 2.0,
	MaximumInterval:    5 * time.Minute,
	MaximumAttempts:    3,
}

// retryPublish retries quickly: it only promotes an already-issued cert
// between Vault paths and never re-runs ACME.
var retryPublish = &temporal.RetryPolicy{
	InitialInterval:    time.Second,
	BackoffCoefficient: 2.0,
	MaximumInterval:    30 * time.Second,
	MaximumAttempts:    5,
}

// -------------------------------------------------------------------------
// WORKFLOW
// -------------------------------------------------------------------------

// CertAcquirer issues the wildcard certificate for the requested domains and
// publishes it to Vault. The schedule input deserializes into req.
func CertAcquirer(ctx workflow.Context, req activities.IssueRequest) error {
	logger := workflow.GetLogger(ctx)
	logger.Info("Starting wildcard certificate acquisition", "domains", req.Domains)

	issueCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout:    15 * time.Minute,
		ScheduleToCloseTimeout: 30 * time.Minute,
		RetryPolicy:            retryIssue,
	})
	if err := workflow.ExecuteActivity(issueCtx, a.IssueWildcardCert, req).Get(issueCtx, nil); err != nil {
		return err
	}

	publishCtx := workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout:    1 * time.Minute,
		ScheduleToCloseTimeout: 5 * time.Minute,
		RetryPolicy:            retryPublish,
	})
	if err := workflow.ExecuteActivity(publishCtx, a.PublishWildcardCert).Get(publishCtx, nil); err != nil {
		return err
	}

	logger.Info("Wildcard certificate acquisition complete", "domains", req.Domains)
	return nil
}
