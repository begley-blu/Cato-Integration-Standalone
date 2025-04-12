## Setup Instructions
* Make the directory: `/etc/cato-cef-forwarder/`
* Setup Systemd Service:
  * Copy `cato-cef-forwarder.service` to `/etc/systemd/system`
  * Enable Service: `systemctl enable cato-cef-forwarder`
  * Reload Systemd Daemons: `systemctl daemon-reload`
* Copy `cato-cef-forwarder` compiled binary to `/usr/local/bin` or a directory of your choice.
* Copy `cato_caller.sh` to `/etc/cato-cef-forwarder/`
* Copy `field_map.json` to `/etc/cato-cef-forwarder/`
* Copy `last_marker.txt` to `/etc/cato-cef-forwarder/`
* Copy `.env` to `/etc/cato-cef-forwarder/`
* Start the service: `systemctl start cato-cef-forwarder`
  * Review `journalctl -fa` output to make sure the command is processing properly.

---

## Configuration Parameters

| Parameter | Default Value | Description |
|-----------|---------------|-------------|
| url | https://api.catonetworks.com/api/v1/graphql2 | Cato Networks API URL |
| token | "" | Cato Networks API Token |
| account | "" | Cato Networks Account ID |
| syslog-proto | tcp | Syslog protocol (udp/tcp) |
| syslog-server | localhost | Syslog server address |
| syslog-port | 514 | Syslog server port |
| cef-vendor | Check Point | CEF vendor field |
| cef-product | Cato Networks SASE Platform | CEF product field |
| cef-version | 1.0 | CEF version field |
| log-level | info | Log level (debug, info, warn, error) |
| interval | 10 | Event fetch interval in seconds |
| marker-file | last_marker.txt | File to store the last event marker |
| field-map | field_map.json | JSON file with field mapping configuration |
| max-msg-size | 8096 | Maximum size of a syslog message |

You can either reference these from the .env file `EnvironmentFile` reference in the systemd unit file or directly via `ENVIRONMENT=`. The API Key should never be stored within the systemd unit file and should be locked down to the service run-as user or root via `chmod 0600`

