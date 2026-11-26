package config

const (
	// The panel asset comes from a GitHub *release*, and upstream cuts those
	// slower than it ships panel pages on dev: the release could lack a page the
	// backend already serves (billing, for one), leaving a dead sidebar entry.
	// This fork mirrors a build of upstream dev as its own latest release, so the
	// panel a relay downloads always matches the backend it runs against.
	DefaultPanelGitHubRepository      = "https://github.com/zuohuadong/codeProxy"
	DefaultPprofAddr                  = "127.0.0.1:8316"
	DefaultAutoUpdateChannel          = "main"
	DefaultAutoUpdateRepository       = "https://github.com/zuohuadong/CliRelay"
	DefaultAutoUpdateDockerImage      = "ghcr.io/zuohuadong/clirelay"
	DefaultAutoUpdateUpdaterURL       = "http://clirelay-updater:8320"
	DefaultModelRequestBodyMB         = 128
	DefaultRequestBodyDiskThresholdMB = 8

	// EnvAuthPath overrides auth-dir with the path visible inside the running container/process.
	EnvAuthPath = "AUTH_PATH"
	// EnvHost overrides the configured listen host. A slot behind a same-host
	// reverse proxy sets 127.0.0.1 so its plain-HTTP port is not reachable
	// from outside. Unlike a config.yaml edit, it does not hot-reload the
	// running process.
	EnvHost = "CLIRELAY_HOST"
	// EnvPort overrides the configured listen port for blue-green deploy slots.
	EnvPort = "CLIRELAY_PORT"
	// EnvLegacyPort keeps the existing Docker installer PORT environment useful.
	EnvLegacyPort = "PORT"
	// EnvPostgresDSN overrides postgres.dsn for container and secret-managed deployments.
	EnvPostgresDSN = "CLIRELAY_POSTGRES_DSN"
)
