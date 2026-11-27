# Run beehest serve

`beehest serve` keeps one engine up on the state folder, in the foreground. It starts no daemon, forks no process and writes no pid file. The service manager of the machine starts it and restarts it.

## What the command does

The command opens the engine and resumes the open runs of this build. It applies the review poll, then waits. The engine fires no other schedule.

`beehest run <item>` on the same state folder gives its item to this engine and prints the result. The `agents.max_concurrent` limit applies to the runs of both commands. The `beehest run` command exits when its item ends, and `beehest serve` stays up.

`beehest serve` takes the same `--config` flag as `beehest run`. It takes no item.

## Stop and restart

SIGINT and SIGTERM stop the engine once and exit with code 0. The open runs stay open, and the next engine resumes them. A second signal during the stop exits at once with code 130.

If another engine holds the state folder, `beehest serve` exits with code 1 and says: "another beehest engine owns this state folder". If the launch fails, it also exits with code 1.

A build that differs from the build of the engine cannot join it. After an upgrade, restart `beehest serve`.

## Check the engine

Run `beehest status`. While the engine runs, the output has the line `engine: serve, pid <N>, since <time>`. Otherwise it has the line `no engine runs`. The lock on the state folder decides which line shows. The file `engine.info` only describes the engine.

## Turn on the review poll

The review poll is off while `agents.review_poll_minutes` is 0. Under `beehest serve`, set `agents.review_poll_minutes` to 60.

## Sample launchd file

Save the file as `~/Library/LaunchAgents/dev.beehest.serve.plist`. Then run `launchctl load ~/Library/LaunchAgents/dev.beehest.serve.plist`.

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>dev.beehest.serve</string>
  <key>ProgramArguments</key>
  <array>
    <string>/usr/local/bin/beehest</string>
    <string>serve</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>BEEHEST_CONFIG</key>
    <string>/Users/you/.config/beehest</string>
    <key>BEEHEST_STATE_DIR</key>
    <string>/Users/you/.local/state/beehest</string>
  </dict>
  <key>KeepAlive</key>
  <true/>
  <key>RunAtLoad</key>
  <true/>
  <key>StandardErrorPath</key>
  <string>/Users/you/Library/Logs/beehest-serve.log</string>
</dict>
</plist>
```

## Sample systemd unit

Save the file as `~/.config/systemd/user/beehest-serve.service`. Then run `systemctl --user enable --now beehest-serve`.

```ini
[Unit]
Description=beehest serve

[Service]
ExecStart=/usr/local/bin/beehest serve
Environment=BEEHEST_CONFIG=%h/.config/beehest
Environment=BEEHEST_STATE_DIR=%h/.local/state/beehest
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
```

`Restart=on-failure` does not restart the unit after `systemctl --user stop`, because the command exits with code 0 on SIGTERM. A second engine that finds the state folder busy exits with code 1, and the unit restarts it every 5 seconds until the first engine stops.
