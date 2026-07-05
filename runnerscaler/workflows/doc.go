// Package workflows implements the runner-scaler workflows. PollAndDispatch is
// the scheduled parent: each tick it loads the per-repo config, lists each
// repo's queued self-hosted jobs through the GitHub client its mode selects,
// reconciles queued depth against the active runners across every dispatched
// job, and starts one HandleRunner child per shortfall runner. A repo's mode
// picks its strategy: "app" polls the GitHub App installation and mints a
// registration token per dispatch; "vault" polls with a PAT from the secret
// store and lets the dispatched job self-register from its own stored
// credential, for repos the App can't be installed on. HandleRunner dispatches
// one ephemeral runner and, via a backstop timer, reaps a runner that never
// picked its job up. All I/O lives in activities; the workflows are pure
// orchestration.
package workflows
