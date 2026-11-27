module github.com/tobocop2/beehest

go 1.27

toolchain go1.27.4

require (
	github.com/bmatcuk/doublestar/v4 v4.10.4
	github.com/dbos-inc/dbos-transact-golang v1.8.0
	github.com/elazarl/goproxy v1.9.2
	github.com/gofrs/flock v0.13.1
	github.com/google/renameio/v2 v2.0.2
	github.com/landlock-lsm/go-landlock v0.11.0
	github.com/mattn/go-isatty v0.0.24
	github.com/rogpeppe/go-internal v1.15.0
	github.com/spf13/cobra v1.10.2
	github.com/stretchr/testify v1.12.1
	go.starlark.net v0.0.0-20261021141710-4f4267fb140d
	go.yaml.in/yaml/v3 v3.0.6
	golang.org/x/mod v0.42.0
	golang.org/x/net v0.61.0
	golang.org/x/sys v0.50.0
	golang.org/x/text v0.44.0
	modernc.org/sqlite v1.62.0
	mvdan.cc/sh/v3 v3.14.1
)

require (
	github.com/dustin/go-humanize v1.1.0 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/jackc/pgerrcode v0.0.0-20250907135507-afb5586c32a6 // indirect
	github.com/jackc/pgpassfile v1.0.0 // indirect
	github.com/jackc/pgservicefile v0.0.0-20240606120523-5a60cdf6a761 // indirect
	github.com/jackc/pgx/v5 v5.13.0 // indirect
	github.com/jackc/puddle/v2 v2.2.4 // indirect
	github.com/ncruces/go-strftime v1.1.0 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	github.com/robfig/cron/v3 v3.0.1 // indirect
	github.com/spf13/pflag v1.1.0 // indirect
	golang.org/x/sync v0.25.0 // indirect
	golang.org/x/tools v0.52.0 // indirect
	kernel.org/pub/linux/libs/security/libcap/psx v1.2.77 // indirect
	modernc.org/libc v1.81.2 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.13.0 // indirect
)

// The fork carries three changes. Commit 4960747 cancels each level of a workflow before it lists the children of that level, so a child that
// starts during a cancel with children is cancelled or refused (tobocop2/dbos-transact-golang#2). Commit e7cfe7c adds WithCancelFailed, a cancel
// that also ends a failed workflow in the same update, which beehest cancel uses for a failed run. Commit ae58a59 adds WithRewindKeepMessages, a
// rewind that keeps the messages of the run (tobocop2/dbos-transact-golang#4), which beehest resume uses. Drop this line when a DBOS release has all three.
replace github.com/dbos-inc/dbos-transact-golang => github.com/tobocop2/dbos-transact-golang v1.7.1-0.20261122032533-ae58a59da255
