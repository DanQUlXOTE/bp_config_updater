# ConfigUpdater

Bulk-add Windows Event sources to an existing Bindplane configuration by
cloning a template source already in the config. Each new source is a remote
WinRM collection target whose hostname/IP (and optional per-row parameter
overrides) come from a CSV. Windows username and password are entered
interactively and shared across every source created in the run.

The inline processor chain attached to the template source is preserved
verbatim on every clone, and v2 `routes` are copied so each new source wires
to the same destinations as the template.

## Requirements

- Go 1.22+ (to build from source).
- A Bindplane server reachable from the machine running the script.
- A Bindplane API key with permission to read and apply configurations, or a
  username/password pair for HTTP basic auth.

## Install

```bash
git clone https://github.com/DanQUlXOTE/bp_config_updater.git
cd bp_config_updater
go build -o configupdater .
```

The resulting `configupdater` binary is self-contained.

## The three modes

| Mode | What it does | When to use |
|------|--------------|-------------|
| `dry-run` | Writes the modified Configuration YAML to a local file. Does not call Bindplane to apply. | First pass — eyeball the diff before anything touches the server. |
| `verify` | POSTs the modified Configuration to Bindplane as a **new** configuration under a name you choose. | Inspect the cloned sources in the Bindplane UI on a throwaway config before updating the real one. |
| `update` | POSTs the modified Configuration back **in place**, replacing the original. Prompts you to retype the config name to confirm. | Final step, after verify-mode looks right. |

## CSV format

- First row is a header. `hostname` is **required**. Every other column
  header is treated as a source parameter to override.
- Optional `name` column becomes the cloned source's `displayName`. If
  omitted, a name is derived from the hostname.
- Any other column whose header matches a parameter on the template source
  (for example `channels`, `remote_domain`, `raw_logs`) will override that
  parameter on the cloned source. Unknown headers produce a warning but do
  not fail the run.

Minimal example:

```csv
hostname
dc01.corp.example.com
dc02.corp.example.com
dc03.corp.example.com
```

With per-row overrides:

```csv
name,hostname,remote_domain
Windows DC — EU 1,dc01.eu.example.com,corp.example.com
Windows DC — EU 2,dc02.eu.example.com,corp.example.com
Windows DC — US 1,dc01.us.example.com,corp.example.com
```

## Authentication

Pick one:

- **API key (recommended).** Put it in an environment variable — default is
  `BINDPLANE_API_KEY`. Override the variable name with `--api-key-env`.
- **Basic auth.** Pass `--user` and `--pass`.

If your `--server` value is a bare hostname, `https://` is prepended
automatically. Use `--skip-tls-verify` for lab deployments with self-signed
certificates.

### Pulling credentials from an existing Bindplane CLI profile

If you already have the `bindplane` CLI configured, its active profile lives
at `~/.bindplane/profiles/<name>.yaml` and contains both the server URL and
the API key:

```bash
PROFILE=~/.bindplane/profiles/$(awk '/^name:/ {print $2}' ~/.bindplane/profiles/current).yaml
export BINDPLANE_API_KEY=$(awk '/apiKey:/ {print $2}' "$PROFILE")
SERVER=$(awk '/remoteURL:/ {print $2}' "$PROFILE")
```

## Flag reference

```
--server              Bindplane server URL (e.g. https://bindplane.example.com)
--config-name         Name of the Bindplane configuration to fetch
--template-source     displayName (preferred) or name of the template windows_event source,
                      or a numeric index into spec.sources
--csv                 Path to the CSV file
--mode                dry-run | verify | update               (default: dry-run)
--out                 Output YAML path for dry-run            (default: <config>-new.yaml)
--api-key-env         Env var holding the API key             (default: BINDPLANE_API_KEY)
--user / --pass       Basic-auth credentials (alternative to API key)
--skip-tls-verify     Skip TLS cert verification (lab only)
--windows-domain      Optional shared Windows domain to set on every new source
--input               Load configuration from a local YAML file instead of the API
```

## Example workflows

### Workflow 1 — Dry-run, then verify, then update (the safe path)

Customer has an existing config `WindowsRemote` that already contains one
`windowsevents_v2` source called **Windows Domain Controllers**. They want to
add 20 more domain controllers from `dcs.csv`.

```bash
export BINDPLANE_API_KEY=bp_01KK...

# 1. Dry-run: produces WindowsRemote-new.yaml locally.
./configupdater \
    --server https://bindplane.example.com \
    --config-name WindowsRemote \
    --template-source "Windows Domain Controllers" \
    --csv dcs.csv \
    --mode dry-run

# Review the diff:
diff <(./configupdater --server https://bindplane.example.com \
         --config-name WindowsRemote --template-source "Windows Domain Controllers" \
         --csv /dev/null --mode dry-run 2>/dev/null) \
     WindowsRemote-new.yaml

# 2. Verify: push to Bindplane as a NEW config for UI review.
./configupdater \
    --server https://bindplane.example.com \
    --config-name WindowsRemote \
    --template-source "Windows Domain Controllers" \
    --csv dcs.csv \
    --mode verify
# Prompts for Windows username + password, then a name for the
# verification config (e.g. "WindowsRemote-verify"). Inspect it in the UI.

# 3. Update: apply the change to the real config.
./configupdater \
    --server https://bindplane.example.com \
    --config-name WindowsRemote \
    --template-source "Windows Domain Controllers" \
    --csv dcs.csv \
    --mode update
# Prompts for credentials again, then asks you to retype
# "WindowsRemote" to confirm before writing.
```

### Workflow 2 — Select template source by numeric index

When the displayName has odd characters or you just want to grab the first
windows_event source:

```bash
./configupdater --server https://bp.local --config-name MyConfig \
                --template-source 0 --csv hosts.csv --mode dry-run
```

### Workflow 3 — Offline dry-run against a saved YAML

Useful when the server is behind a bastion or you want to iterate quickly on
CSV tweaks without re-hitting the API:

```bash
# Save the current config once.
curl -H "X-Bindplane-Api-Key: $BINDPLANE_API_KEY" \
     https://bindplane.example.com/v1/configurations/WindowsRemote \
     | jq -r .configuration > windows-remote.yaml

# Then run dry-runs entirely offline.
./configupdater --input windows-remote.yaml \
                --template-source "Windows Domain Controllers" \
                --csv hosts.csv --mode dry-run --out preview.yaml
```

### Workflow 4 — Shared Windows domain for every new source

```bash
./configupdater --server https://bp.local --config-name WindowsRemote \
                --template-source "Windows DCs" --csv hosts.csv \
                --windows-domain corp.example.com --mode verify
```

## What the script does to each cloned source

1. Deep-copies the template source's full map (parameters, inline
   processors, v2 routes, everything else).
2. Clears `id` and `name` so Bindplane mints fresh IDs on apply.
3. Sets `displayName` from the CSV `name` column (or a sanitized form of the
   hostname).
4. Forces `use_remote=true` and writes `remote_server`, `remote_username`,
   `remote_password`, and optionally `remote_domain` from the CSV and the
   interactive prompts.
5. Applies any other CSV column values as parameter overrides on the
   matching parameter name.
6. Appends the clone to `spec.sources`. Top-level `spec.processors` and
   `spec.destinations` are left untouched so every new source flows through
   the same pipeline.

## Security notes

- The Windows password is read with echo disabled when stdin is a TTY.
- Dry-run output YAML contains the Windows password in plaintext in the
  `remote_password` parameter. The file is written with mode `0600`. Delete
  it after you're done reviewing.
- The API key is read from an environment variable by default to keep it
  out of shell history.

## Development

```bash
go test ./...        # run unit tests
go vet ./...         # static checks
go build -o configupdater .
```

Tests in `internal/cloner/cloner_test.go` cover v1 append, v2 route copy,
inline-processor preservation, displayName-based template lookup, and CSV
parsing edge cases.
