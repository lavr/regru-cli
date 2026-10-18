# regru

An unofficial command line tool for managing DNS zones hosted on reg.ru through the REG.API v2.
It is not affiliated with or endorsed by reg.ru.

## Install

The repository is private, so `go install ...@latest` does not work without extra Go proxy settings.
Build from a checkout instead, Go 1.26 or newer:

    git clone <repository url> regru-cli
    cd regru-cli
    make build

This produces `./regru`; copy it anywhere on your `PATH`.

`make cross` builds binaries for linux and darwin on amd64 and arm64 into `dist/`.

## Configuration

The config file lives at `~/.config/regru/config.yaml` (honours `XDG_CONFIG_HOME`; override with `--config`
or `REGRU_CONFIG`). Passwords are never stored in it, only `env:` references:

    version: 1
    default_account: main
    accounts:
      main:
        username: my-login
        password: env:REGRU_PASSWORD_MAIN
      other:
        username: env:REGRU_USERNAME_OTHER
        password: env:REGRU_PASSWORD_OTHER
        env_file: ~/.config/regru/secrets.env
    defaults:
      max_deletes: 10
      timeout: 5m
      request_timeout: 30s

`password` must be an `env:VAR` reference; a literal password makes the whole config invalid (exit code 2).
`username` is either a literal login or an `env:VAR` reference; an empty login is an error. Variables are
looked up in the process environment first, then in `env_file`. Without a config file, the
`REGRU_USERNAME` and `REGRU_PASSWORD` environment variables are used.

Choose an account with `--account NAME` or `REGRU_ACCOUNT`, otherwise `default_account` is used.
`--account auto` finds the account that owns the domain given on the command line by listing the domains
of every configured account. The result is cached for an hour in `~/.cache/regru/domains.json`
(`XDG_CACHE_HOME`); commands that change a zone always look the domain up again.

    regru account list
    regru account check
    regru --account auto dns get example.test

`account list` shows the configured accounts and their variable references, never secrets.
`account check` logs in and prints the login, user id and whether the account is a reseller.

Requests are rate limited to one every 3 seconds. Journals, caches and locks are kept under the XDG
state and cache directories with owner-only permissions.

## Usage

    regru version
    regru domain list
    regru domain get example.test
    regru dns get example.test
    regru dns get example.test --name www --type A

    regru dns export example.test --file zone.yaml
    regru dns export example.test --scope managed-zone --file zone.yaml
    regru dns plan --file zone.yaml --save plan.json
    regru dns plan --file zone.yaml --detailed-exitcode
    regru dns apply --plan plan.json
    regru dns apply --file zone.yaml --dry-run

    regru dns add example.test www A 192.0.2.10
    regru dns rm example.test www A 192.0.2.10
    regru dns rm example.test old A --all-values
    regru dns set example.test www A 192.0.2.10 192.0.2.11
    regru dns set example.test @ TXT --yes -- "-all"

Every change is planned, shown, confirmed (or `--yes`), verified against the current zone right before
it is sent, and checked by reading the zone afterwards. Deletions are capped by `--max-deletes`, and
a plan that would leave a zone without writable records is refused unless `--allow-empty-zone` is given.
The apex NS records are never changed. Removing or replacing the last writable record of a zone with
`dns rm` or `dns set` therefore also needs `--allow-empty-zone`.

Record names are relative to the zone (`www`, `@`, `_sip._tcp`, `*`). A fully qualified name needs a
trailing dot (`www.example.test.`); a relative name that ends with the zone name is refused as ambiguous.

`dns rm --all-values` removes every value of the RRset as read right before planning.

A manifest has a scope. `listed-rrsets` (the default for `dns export`) only touches the RRsets listed in
`rrsets`. `managed-zone` also removes writable records missing from the manifest. Records the tool cannot
manage exactly (unsupported types, duplicates, an RRset that mixes normal and such records) are exported
under `readonly` and are never changed.

Each applied change is recorded in a journal, one JSON line per step, at
`~/.local/state/regru/journal/<user_id>/<zone>/<time>-<run_id>.jsonl` (`XDG_STATE_HOME`). The 50 most
recent completed runs per zone are kept; runs that ended partially or with an unknown outcome are never
rotated away.

`--output json` prints exactly one JSON document to stdout:
`{"schema_version":1,"status":...,"data":...,"errors":[...],"warnings":[...]}`.

## Exit codes

| code | meaning |
|---|---|
| 0 | success or nothing to do |
| 1 | error, API refusal, or confirmation required |
| 2 | bad arguments or configuration |
| 3 | partially applied, unknown outcome, or verification mismatch |
| 4 | plan is stale or incompatible with the current zone |

`dns plan --detailed-exitcode` returns 0 for no changes, 2 for changes, 1 for any error.

## API behavior

What the API was proven to do (exact record deletion, record formats, error shapes) and the limits of
those checks are in [docs/api-probes.md](docs/api-probes.md).

## Development

    make test lint cross

Live tests run against a real zone and are excluded from the normal build. They need an explicit opt-in:

    REGRU_LIVE_ZONE=<zone> REGRU_LIVE_ALLOW_WRITE=1 REGRU_USERNAME=<login> REGRU_PASSWORD=<password> \
      go test -tags live ./internal/regruzone/ -run Live -v

They create and remove records only under a random `regru-live-<id>` prefix and compare the zone with its
baseline at the end. Use a throwaway zone.
