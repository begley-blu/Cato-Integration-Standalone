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

Config parameters are listed in `main.go` here: https://github.com/begley-blu/Cato-Integration-Standalone/blob/d6db50566e3a2ea2481833fec9dcee3f8863b190/main.go#L297

You can either reference these from the .env file `EnvironmentFile` reference in the systemd unit file or directly via `ENVIRONMENT=`. The API Key should never be stored within the systemd unit file and should be locked down to the service run-as user or root via `chmod 0600`

