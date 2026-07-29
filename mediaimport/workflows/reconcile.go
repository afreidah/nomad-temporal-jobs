// -------------------------------------------------------------------------------
// Media Import Workflow - Reconcile Completed Downloads into the Library
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Pure orchestration: list the completed Deluge torrents, reconcile each into
// Sonarr (TV) or, on no series match, Radarr (movies) with bounded concurrency,
// then trigger a single Jellyfin scan if anything was imported. All I/O lives in
// the activities; the workflow only fans out and aggregates.
// -------------------------------------------------------------------------------

package workflows

import (
	"go.temporal.io/sdk/workflow"

	"munchbox/temporal-workers/mediaimport/activities"
	"munchbox/temporal-workers/shared"
)

// a is a nil handle used only to register activities by method value.
var a *activities.Activities

// Reconcile imports manually-downloaded, completed torrents into the library.
func Reconcile(ctx workflow.Context, config activities.ReconcileConfig) error {
	logger := workflow.GetLogger(ctx)
	config.ApplyDefaults()
	logger.Info("Starting media import reconcile",
		"concurrency", config.Concurrency, "dry_run", config.DryRun)

	quickCtx := workflow.WithActivityOptions(ctx, shared.QuickActivityOptions())

	var folders []string
	if err := workflow.ExecuteActivity(quickCtx, a.ListCompleted).Get(quickCtx, &folders); err != nil {
		return err
	}
	logger.Info("Completed torrents to reconcile", "count", len(folders))

	results := make([]activities.ImportResult, len(folders))
	sem := workflow.NewBufferedChannel(ctx, config.Concurrency)
	wg := workflow.NewWaitGroup(ctx)
	for i, folder := range folders {
		wg.Add(1)
		workflow.Go(ctx, func(gctx workflow.Context) {
			defer wg.Done()
			sem.Send(gctx, nil)
			defer sem.Receive(gctx, nil)
			results[i] = reconcileFolder(gctx, folder, config.DryRun)
		})
	}
	wg.Wait(ctx)

	imported, skipped := 0, 0
	var flagged []string
	for _, r := range results {
		imported += r.Imported
		skipped += r.Skipped
		if r.NoMatch && r.App == "radarr" {
			// no series AND no movie match anywhere -> needs a human.
			flagged = append(flagged, r.Folder)
		}
		flagged = append(flagged, r.Flagged...)
	}

	if imported > 0 && !config.DryRun {
		refreshCtx := workflow.WithActivityOptions(ctx, shared.QuickActivityOptions())
		if err := workflow.ExecuteActivity(refreshCtx, a.JellyfinRefresh).Get(refreshCtx, nil); err != nil {
			logger.Warn("Jellyfin refresh failed", "error", err)
		}
	}

	logger.Info("Media import reconcile complete",
		"imported", imported, "skipped", skipped, "flagged", len(flagged))
	if len(flagged) > 0 {
		logger.Warn("Downloads needing manual attention (unknown series/movie or unmappable)",
			"folders", flagged)
	}
	return nil
}

// reconcileFolder tries Sonarr first; if no series matched, tries Radarr.
func reconcileFolder(ctx workflow.Context, folder string, dryRun bool) activities.ImportResult {
	logger := workflow.GetLogger(ctx)
	opts := shared.QuickActivityOptions()
	req := activities.ImportRequest{Folder: folder, DryRun: dryRun}

	sonarrCtx := workflow.WithActivityOptions(ctx, withActivityID(opts, "Sonarr:"+folder))
	var res activities.ImportResult
	if err := workflow.ExecuteActivity(sonarrCtx, a.SonarrImport, req).Get(sonarrCtx, &res); err != nil {
		logger.Warn("Sonarr import errored", "folder", folder, "error", err)
		return activities.ImportResult{Folder: folder, App: "sonarr", Flagged: []string{folder}}
	}
	if !res.NoMatch {
		return res
	}

	radarrCtx := workflow.WithActivityOptions(ctx, withActivityID(opts, "Radarr:"+folder))
	var mres activities.ImportResult
	if err := workflow.ExecuteActivity(radarrCtx, a.RadarrImport, req).Get(radarrCtx, &mres); err != nil {
		logger.Warn("Radarr import errored", "folder", folder, "error", err)
		return activities.ImportResult{Folder: folder, App: "radarr", Flagged: []string{folder}}
	}
	return mres
}

func withActivityID(opts workflow.ActivityOptions, id string) workflow.ActivityOptions {
	opts.ActivityID = id
	return opts
}
