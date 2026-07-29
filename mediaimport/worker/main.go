// -------------------------------------------------------------------------------
// Media Import Worker - Entry Point
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Polls media-import-task-queue and runs the Reconcile workflow. Authenticates
// to Vault with its Nomad Workload Identity and pulls the Sonarr/Radarr/Deluge/
// Jellyfin credentials through it; service endpoints come from env with
// service.consul defaults.
// -------------------------------------------------------------------------------

package main

import (
	"cmp"
	"context"
	"log"
	"log/slog"
	"os"

	"go.temporal.io/sdk/worker"

	"munchbox/temporal-workers/mediaimport/activities"
	"munchbox/temporal-workers/mediaimport/workflows"
	"munchbox/temporal-workers/shared"
	"munchbox/temporal-workers/shared/client/vault"
)

func main() {
	err := shared.RunWorker(context.Background(), shared.WorkerSpec{
		Service:   "media-import-worker",
		TaskQueue: "media-import-task-queue",
		Register: func(ctx context.Context, slogger *slog.Logger, w worker.Worker) (func(), error) {
			vc, err := vault.NewVaultWithRefresher(ctx, slogger)
			if err != nil {
				return nil, err
			}
			acts, err := activities.New(ctx, activities.Config{
				Vault:        vc,
				SonarrAddr:   cmp.Or(os.Getenv("SONARR_ADDR"), "http://sonarr.service.consul:8989"),
				RadarrAddr:   cmp.Or(os.Getenv("RADARR_ADDR"), "http://radarr.service.consul:7878"),
				DelugeAddr:   cmp.Or(os.Getenv("DELUGE_ADDR"), "http://deluge.service.consul:8112"),
				JellyfinAddr: cmp.Or(os.Getenv("JELLYFIN_ADDR"), "http://jellyfin.service.consul:8096"),
			})
			if err != nil {
				return nil, err
			}
			w.RegisterWorkflow(workflows.Reconcile)
			w.RegisterActivity(acts)
			return nil, nil
		},
	})
	if err != nil {
		log.Fatalln(err)
	}
}
