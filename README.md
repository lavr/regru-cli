# regru

An unofficial command line tool for managing DNS zones hosted on reg.ru through the REG.API v2.
It is not affiliated with or endorsed by reg.ru.

## Install

From source, Go 1.26 or newer:

    go install github.com/lavr/regru-cli/cmd/regru@latest

or from a checkout:

    make build

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
        env_file: ~/.config/regru/secrets.env
    defaults:
      max_deletes: 10
      timeout: 5m
      request_timeout: 30s

The variable is looked up in the process environment first, then in `env_file`. Without a config file,
the `REGRU_USERNAME` and `REGRU_PASSWORD` environment variables are used. Choose an account with `--account`.

Requests are rate limited to one every 3 seconds. Journals, caches and locks are kept under the XDG
state and cache directories with owner-only permissions.

## Usage

    regru domain list
    regru dns get example.test
    regru dns get example.test --name www --type A

    regru dns export example.test --file zone.yaml
    regru dns plan --file zone.yaml --save plan.json
    regru dns plan --file zone.yaml --detailed-exitcode
    regru dns apply --plan plan.json
    regru dns apply --file zone.yaml --dry-run

    regru dns add example.test www A 192.0.2.10
    regru dns rm example.test www A 192.0.2.10
    regru dns set example.test www A 192.0.2.10 192.0.2.11
    regru dns set example.test @ TXT --yes -- "-all"

Every change is planned, shown, confirmed (or `--yes`), verified against the current zone right before
it is sent, and checked by reading the zone afterwards. Deletions are capped by `--max-deletes`, and
a plan that would leave a zone without writable records is refused unless `--allow-empty-zone` is given.
The apex NS records are never changed.

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
