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

I'll update the configuration parameters section to match the environment variables used in the code and remove the Parameter column as requested.


| Environment Variable | Default Value | Description |
|-----------|---------------|-------------|
| CATO_API_URL | https://api.catonetworks.com/api/v1/graphql2 | Cato Networks API URL |
| CATO_API_TOKEN | "" | Cato Networks API Token |
| CATO_ACCOUNT_ID | "" | Cato Networks Account ID |
| SYSLOG_PROTOCOL | tcp | Syslog protocol (udp/tcp) |
| SYSLOG_SERVER | localhost | Syslog server address |
| SYSLOG_PORT | 514 | Syslog server port |
| CEF_VENDOR | Check Point | CEF vendor field |
| CEF_PRODUCT | Cato Networks SASE Platform | CEF product field |
| CEF_VERSION | 1.0 | CEF version field |
| LOG_LEVEL | info | Log level (debug, info, warn, error) |
| FETCH_INTERVAL | 10 | Event fetch interval in seconds |
| MARKER_FILE | last_marker.txt | File to store the last event marker |
| FIELD_MAP_FILE | field_map.json | JSON file with field mapping configuration |
| MAX_MSG_SIZE | 8096 | Maximum size of a syslog message |

You can either reference these from the .env file `EnvironmentFile` reference in the systemd unit file or directly via `ENVIRONMENT=`. The API Key should never be stored within the systemd unit file and should be locked down to the service run-as user or root via `chmod 0600`

