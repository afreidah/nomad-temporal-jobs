// -------------------------------------------------------------------------------
// Media Import Activities - Reconcile Deluge Downloads into the Sonarr/Radarr Library
//
// Project: Nomad Temporal Jobs / Author: Alex Freidah
//
// Activities that reconcile manually-downloaded torrents (grabbed outside
// Sonarr/Radarr) into the media library so Jellyfin can see them. Deluge is
// queried for completed torrents; each is offered to Sonarr (TV) and, failing a
// series match, Radarr (movies) via their manual-import API, which hardlinks the
// genuinely-missing episodes/movie into the library. In-progress torrents,
// duplicates, and quality-downgrades are skipped; multi-season packs (which
// Sonarr's auto-import refuses) are force-imported with explicit mappings.
// -------------------------------------------------------------------------------

package activities

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"munchbox/temporal-workers/shared"
)

// --- Vault paths + fields the worker reads for the downstream service creds ---
const (
	vaultMediaImportPath = "media-import"
	vaultDelugePath      = "deluge"
	vaultJellyfinPath    = "jellyfin"

	fieldSonarrAPIKey = "sonarr_api_key"
	fieldRadarrAPIKey = "radarr_api_key"
	fieldDelugePass   = "web_password"
	fieldJellyfinKey  = "api_key"
	completeProgress  = 100.0
	attrMediaFolder   = "media.folder"
)

// secretReader is the narrow slice of the Vault client this worker needs.
type secretReader interface {
	ReadKVField(ctx context.Context, path, field string) (string, error)
}

// httpDoer is the narrow HTTP surface the activities call, so tests can inject a
// stub (or an httptest server's client) without depending on *http.Client.
type httpDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Config is the worker-level configuration wired from env + Vault in main.
type Config struct {
	Vault        secretReader
	SonarrAddr   string
	RadarrAddr   string
	DelugeAddr   string
	JellyfinAddr string
}

// Validate ensures the endpoints and Vault handle are present.
func (c Config) Validate() error {
	if c.Vault == nil {
		return errors.New("vault is required")
	}
	for name, v := range map[string]string{
		"SonarrAddr": c.SonarrAddr, "RadarrAddr": c.RadarrAddr,
		"DelugeAddr": c.DelugeAddr, "JellyfinAddr": c.JellyfinAddr,
	} {
		if v == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	return nil
}

// Activities holds the resolved credentials and the shared HTTP client used for
// every downstream call. The cookie jar carries Deluge's session across calls.
type Activities struct {
	cfg         Config
	http        httpDoer
	sonarrKey   string
	radarrKey   string
	delugePass  string
	jellyfinKey string
}

// New resolves the downstream credentials from Vault and builds the client.
func New(ctx context.Context, cfg Config) (*Activities, error) {
	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("invalid config: %w", err)
	}
	sonarrKey, err := cfg.Vault.ReadKVField(ctx, vaultMediaImportPath, fieldSonarrAPIKey)
	if err != nil {
		return nil, fmt.Errorf("read sonarr api key: %w", err)
	}
	radarrKey, err := cfg.Vault.ReadKVField(ctx, vaultMediaImportPath, fieldRadarrAPIKey)
	if err != nil {
		return nil, fmt.Errorf("read radarr api key: %w", err)
	}
	delugePass, err := cfg.Vault.ReadKVField(ctx, vaultDelugePath, fieldDelugePass)
	if err != nil {
		return nil, fmt.Errorf("read deluge password: %w", err)
	}
	jellyfinKey, err := cfg.Vault.ReadKVField(ctx, vaultJellyfinPath, fieldJellyfinKey)
	if err != nil {
		return nil, fmt.Errorf("read jellyfin api key: %w", err)
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, fmt.Errorf("cookie jar: %w", err)
	}
	return &Activities{
		cfg:         cfg,
		http:        &http.Client{Timeout: 2 * time.Minute, Jar: jar},
		sonarrKey:   sonarrKey,
		radarrKey:   radarrKey,
		delugePass:  delugePass,
		jellyfinKey: jellyfinKey,
	}, nil
}

// ReconcileConfig is the workflow input (schedule JSON) controlling a run.
type ReconcileConfig struct {
	// Concurrency bounds how many folders reconcile in parallel.
	Concurrency int `json:"concurrency"`
	// DryRun reports what would import without hardlinking anything.
	DryRun bool `json:"dry_run"`
}

// ApplyDefaults fills unset fields with safe defaults.
func (c *ReconcileConfig) ApplyDefaults() {
	if c.Concurrency <= 0 {
		c.Concurrency = 4
	}
}

// ImportRequest is a single folder offered to Sonarr or Radarr.
type ImportRequest struct {
	Folder string `json:"folder"`
	DryRun bool   `json:"dry_run"`
}

// ImportResult is the per-folder outcome the workflow aggregates.
type ImportResult struct {
	Folder   string   `json:"folder"`
	App      string   `json:"app"`
	Imported int      `json:"imported"`
	Skipped  int      `json:"skipped"`
	Flagged  []string `json:"flagged"`
	NoMatch  bool     `json:"no_match"`
}

// -------------------------------------------------------------------------------
// Deluge
// -------------------------------------------------------------------------------

type delugeRequest struct {
	Method string `json:"method"`
	Params []any  `json:"params"`
	ID     int    `json:"id"`
}

type torrentStatus struct {
	Name     string  `json:"name"`
	Progress float64 `json:"progress"`
	State    string  `json:"state"`
}

// ListCompleted returns the download names of every torrent that has finished
// downloading (100%). In-progress torrents are excluded so their partial files
// are never linked into the library.
func (a *Activities) ListCompleted(ctx context.Context) ([]string, error) {
	ctx, span := shared.StartPeerSpan(ctx, "deluge", "deluge.list_completed")
	defer span.End()

	if err := a.delugeLogin(ctx); err != nil {
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}
	var raw struct {
		Result map[string]torrentStatus `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	err := a.delugeCall(ctx, delugeRequest{
		Method: "core.get_torrents_status",
		Params: []any{map[string]any{}, []string{"name", "progress", "state"}},
		ID:     2,
	}, &raw)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}
	if raw.Error != nil {
		err := fmt.Errorf("deluge get_torrents_status: %s", raw.Error.Message)
		span.SetStatus(codes.Error, err.Error())
		return nil, err
	}
	var done []string
	for _, t := range raw.Result {
		if t.Progress >= completeProgress {
			done = append(done, t.Name)
		}
	}
	span.SetAttributes(attribute.Int("deluge.completed", len(done)))
	return done, nil
}

func (a *Activities) delugeLogin(ctx context.Context) error {
	var raw struct {
		Result bool `json:"result"`
	}
	if err := a.delugeCall(ctx, delugeRequest{
		Method: "auth.login", Params: []any{a.delugePass}, ID: 1,
	}, &raw); err != nil {
		return err
	}
	if !raw.Result {
		return errors.New("deluge auth.login rejected the password")
	}
	return nil
}

func (a *Activities) delugeCall(ctx context.Context, req delugeRequest, out any) error {
	body, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("marshal deluge request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		a.cfg.DelugeAddr+"/json", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build deluge request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	resp, err := a.http.Do(httpReq)
	if err != nil {
		return fmt.Errorf("deluge %s: %w", req.Method, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("deluge %s: status %d", req.Method, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode deluge %s: %w", req.Method, err)
	}
	return nil
}

// -------------------------------------------------------------------------------
// Sonarr / Radarr manual import
// -------------------------------------------------------------------------------

// arrDecision is one file's proposed import decision from the manualimport API.
type arrDecision struct {
	Path     string          `json:"path"`
	Series   *arrRef         `json:"series"`
	Movie    *arrRef         `json:"movie"`
	Episodes []arrEpisode    `json:"episodes"`
	Quality  json.RawMessage `json:"quality"`
	Langs    json.RawMessage `json:"languages"`
	Release  string          `json:"releaseGroup"`
	IdxFlags int             `json:"indexerFlags"`
	Rejects  []arrRejection  `json:"rejections"`
}

type arrRef struct {
	ID    int    `json:"id"`
	Title string `json:"title"`
}

type arrEpisode struct {
	ID int `json:"id"`
}

type arrRejection struct {
	Reason string `json:"reason"`
}

// arrImportFile is one entry in the ManualImport command payload.
type arrImportFile struct {
	Path         string          `json:"path"`
	SeriesID     int             `json:"seriesId,omitempty"`
	MovieID      int             `json:"movieId,omitempty"`
	EpisodeIDs   []int           `json:"episodeIds,omitempty"`
	Quality      json.RawMessage `json:"quality"`
	Languages    json.RawMessage `json:"languages"`
	ReleaseGroup string          `json:"releaseGroup"`
	IndexerFlags int             `json:"indexerFlags"`
}

// classifyRejections decides what to do with a file's rejection reasons.
// Empty rejections or only the multi-season-pack "unexpected" guard are
// overridable (we force-import). A quality "not an upgrade" (we already own it
// at equal/better quality) or anything else is a genuine skip.
func importable(d *arrDecision) bool {
	for _, r := range d.Rejects {
		if !strings.Contains(strings.ToLower(r.Reason), "unexpected considering") {
			return false
		}
	}
	return true
}

// SonarrImport offers a folder to Sonarr, force-importing the genuinely-missing
// episodes. NoMatch is set when Sonarr recognizes no series, so the workflow can
// try Radarr instead.
func (a *Activities) SonarrImport(ctx context.Context, req ImportRequest) (ImportResult, error) {
	ctx, span := shared.StartPeerSpan(ctx, "sonarr", "sonarr.manual_import",
		attribute.String(attrMediaFolder, req.Folder))
	defer span.End()

	res := ImportResult{Folder: req.Folder, App: "sonarr"}
	decisions, err := a.arrDecisions(ctx, a.cfg.SonarrAddr, a.sonarrKey, req.Folder)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		return res, err
	}
	var files []arrImportFile
	anySeries := false
	for i := range decisions {
		d := &decisions[i]
		if d.Series == nil || len(d.Episodes) == 0 {
			continue
		}
		anySeries = true
		if !importable(d) {
			res.Skipped++
			continue
		}
		epIDs := make([]int, 0, len(d.Episodes))
		for _, e := range d.Episodes {
			epIDs = append(epIDs, e.ID)
		}
		files = append(files, arrImportFile{
			Path: d.Path, SeriesID: d.Series.ID, EpisodeIDs: epIDs,
			Quality: d.Quality, Languages: d.Langs,
			ReleaseGroup: d.Release, IndexerFlags: d.IdxFlags,
		})
	}
	res.NoMatch = !anySeries
	res.Imported = len(files)
	if len(files) > 0 && !req.DryRun {
		if err := a.arrManualImport(ctx, a.cfg.SonarrAddr, a.sonarrKey, files); err != nil {
			span.SetStatus(codes.Error, err.Error())
			return res, err
		}
	}
	return res, nil
}

// RadarrImport offers a folder to Radarr, force-importing the movie if missing.
func (a *Activities) RadarrImport(ctx context.Context, req ImportRequest) (ImportResult, error) {
	ctx, span := shared.StartPeerSpan(ctx, "radarr", "radarr.manual_import",
		attribute.String(attrMediaFolder, req.Folder))
	defer span.End()

	res := ImportResult{Folder: req.Folder, App: "radarr"}
	decisions, err := a.arrDecisions(ctx, a.cfg.RadarrAddr, a.radarrKey, req.Folder)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		return res, err
	}
	var files []arrImportFile
	anyMovie := false
	for i := range decisions {
		d := &decisions[i]
		if d.Movie == nil {
			continue
		}
		anyMovie = true
		if !importable(d) {
			res.Skipped++
			continue
		}
		files = append(files, arrImportFile{
			Path: d.Path, MovieID: d.Movie.ID,
			Quality: d.Quality, Languages: d.Langs,
			ReleaseGroup: d.Release, IndexerFlags: d.IdxFlags,
		})
	}
	res.NoMatch = !anyMovie
	res.Imported = len(files)
	if len(files) > 0 && !req.DryRun {
		if err := a.arrManualImport(ctx, a.cfg.RadarrAddr, a.radarrKey, files); err != nil {
			span.SetStatus(codes.Error, err.Error())
			return res, err
		}
	}
	return res, nil
}

func (a *Activities) arrDecisions(ctx context.Context, addr, key, folder string) ([]arrDecision, error) {
	q := url.Values{}
	q.Set("folder", "/data/torrents/"+folder)
	q.Set("filterExistingFiles", "true")
	endpoint := addr + "/api/v3/manualimport?" + q.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build manualimport request: %w", err)
	}
	req.Header.Set("X-Api-Key", key)
	resp, err := a.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("manualimport GET: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("manualimport GET: status %d: %s", resp.StatusCode, b)
	}
	var decisions []arrDecision
	if err := json.NewDecoder(resp.Body).Decode(&decisions); err != nil {
		return nil, fmt.Errorf("decode manualimport: %w", err)
	}
	return decisions, nil
}

func (a *Activities) arrManualImport(ctx context.Context, addr, key string, files []arrImportFile) error {
	payload := map[string]any{"name": "ManualImport", "importMode": "Copy", "files": files}
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal ManualImport: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		addr+"/api/v3/command", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build ManualImport request: %w", err)
	}
	req.Header.Set("X-Api-Key", key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.http.Do(req)
	if err != nil {
		return fmt.Errorf("ManualImport POST: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("ManualImport POST: status %d: %s", resp.StatusCode, b)
	}
	return nil
}

// -------------------------------------------------------------------------------
// Jellyfin
// -------------------------------------------------------------------------------

// JellyfinRefresh triggers a full library scan so freshly-imported media appears.
func (a *Activities) JellyfinRefresh(ctx context.Context) error {
	ctx, span := shared.StartPeerSpan(ctx, "jellyfin", "jellyfin.library_refresh")
	defer span.End()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		a.cfg.JellyfinAddr+"/Library/Refresh", nil)
	if err != nil {
		return fmt.Errorf("build jellyfin refresh: %w", err)
	}
	req.Header.Set("X-Emby-Token", a.jellyfinKey)
	resp, err := a.http.Do(req)
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("jellyfin refresh: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		err := fmt.Errorf("jellyfin refresh: status %d", resp.StatusCode)
		span.SetStatus(codes.Error, err.Error())
		return err
	}
	return nil
}
