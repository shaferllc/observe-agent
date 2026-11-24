# observe-agent

A small Linux agent that sends a server's CPU, memory, swap, disk, load, network, uptime and watched processes to [Observe](https://observe.live) once a minute. Observe charts them on the server's page and alerts when a number stays over a threshold you set.

- Static Go binary, no dependencies. Linux on amd64 or arm64, with systemd.
- It only sends. It opens no ports, and nothing connects to it.
- If Observe can't be reached, it buffers up to a day of samples and sends them when it can.

Full guide: <https://observe.live/docs/server-agent>

## Install

1. In Observe, add the server under **Monitors → Add server** and turn on its heartbeat.
2. Open the server's page and copy the install command from the **Server agent** panel. It looks like this:

   ```sh
   curl -fsSL https://observe.live/agent/install.sh | sudo sh -s -- --token YOUR_HEARTBEAT_TOKEN
   ```

3. Run it on the server as root. Charts appear within two minutes.

Use the install script Observe serves, not the `install.sh` in this repo: this copy is a template, and Observe fills in its URLs and the systemd unit when it serves it.

The script:

- picks `observe-agent_linux_amd64` or `observe-agent_linux_arm64` from the latest release here;
- downloads it with `checksums.txt` and stops if the SHA-256 doesn't match;
- installs it to `/usr/local/bin/observe-agent`;
- writes the URL and token to `/etc/observe-agent.env` (mode 600, root only);
- installs and starts `observe-agent.service`.

Run the same command again to upgrade. Without `--token` it reuses the token already in `/etc/observe-agent.env`.

### Manual install

Download the binary for your architecture and `checksums.txt` from the [latest release](https://github.com/shaferllc/observe-agent/releases/latest), check it with `sha256sum -c --ignore-missing checksums.txt`, install it to `/usr/local/bin/observe-agent`, and run it with the settings below (see `observe-agent.service` for the unit).

## Configuration

The agent reads environment variables (the service loads them from `/etc/observe-agent.env`):

| Variable | Meaning |
| --- | --- |
| `OBSERVE_TOKEN` | Required. The server's heartbeat token from its page in Observe. |
| `OBSERVE_URL` | Where to send samples. Defaults to `https://observe.live`. |
| `OBSERVE_PROCESSES` | Only for `-once`: space-separated process names to check. Normally Observe sends this list back with each reply, from the server's settings. |

Flags:

- `observe-agent -once` prints one sample as JSON and exits without sending anything, so you can see exactly what it reads.
- `observe-agent -version` prints the version.

Observe can change how often the agent samples (60 seconds by default) in its reply, without a new agent.

## What it collects and sends

Once a minute it reads:

| Metric | Source |
| --- | --- |
| CPU | Percent busy across all cores over the last minute (`/proc/stat`). |
| Load | 1, 5 and 15-minute load averages (`/proc/loadavg`). |
| Memory, swap | Used and total (`/proc/meminfo`; used memory is total minus available, as `free` shows it). |
| Disk | Used, size and inodes of each real filesystem (`/proc/mounts` and `statfs`), skipping tmpfs, overlay, snap images, and network or container mounts. At most 32. |
| Network | Bytes per second in and out, summed over every interface but loopback (`/proc/net/dev`). |
| Uptime | Seconds since boot (`/proc/uptime`). |
| Processes | Whether each process name Observe asks about is running (process names from `/proc/<pid>/comm`). |

With each batch it also sends the agent version, the hostname, OS, architecture and kernel release.

It reads nothing else: no file contents, logs, command lines or environment of other processes.

It sends a JSON `POST` to `$OBSERVE_URL/api/agent/v1/metrics` with the token as a Bearer token. That is the only network connection it makes.

## Uninstall

```sh
sudo systemctl disable --now observe-agent
sudo rm /etc/systemd/system/observe-agent.service /usr/local/bin/observe-agent /etc/observe-agent.env
sudo systemctl daemon-reload
```

Then turn off the server's heartbeat in Observe, or delete the server, so the token stops working.

## Security

- The token is only a heartbeat token: it can post samples for that one server and nothing else. It lives in `/etc/observe-agent.env`, readable by root only. Turning off the heartbeat in Observe revokes it.
- The service runs as an unprivileged dynamic user with `NoNewPrivileges`, a read-only filesystem (`ProtectSystem=strict`, `ProtectHome=read-only`) and a private `/tmp`.
- The install script checks the binary against `checksums.txt` from the same release. That catches a corrupted or truncated download, but not a compromised release, since both files come from the same place. If that matters to you, build from source (`go build`) and compare.
- To report a security issue, email support@observe.live (see [observe.live/security](https://observe.live/security)) rather than opening a public issue.

## Building

```sh
go test ./...
go build -o observe-agent .
```

Releases are built with [GoReleaser](https://goreleaser.com) (`.goreleaser.yaml`): static Linux binaries for amd64 and arm64, plus `checksums.txt`.

This repository is published from the Observe monorepo, where the agent is developed; pull requests here are welcome and get applied there.

## License

MIT, see [LICENSE](LICENSE).
