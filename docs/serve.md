# Run beehave serve

`beehave serve` keeps one engine up on the state folder, in the foreground. It starts no daemon, forks no process and writes no pid file. The service manager of the machine starts it and restarts it.

## What the command does

The command opens the engine and resumes the open runs of this build. It applies the review poll and the project schedules, then waits.

`beehave run <item>` on the same state folder gives its item to this engine and prints the result. The `agents.max_concurrent` limit applies to the runs of both commands. The `beehave run` command exits when its item ends, and `beehave serve` stays up.

`beehave serve` takes the same `--config` flag as `beehave run`. It takes no item.

## Stop and restart

SIGINT and SIGTERM stop the engine once and exit with code 0. The open runs stay open, and the next engine resumes them. A second signal during the stop exits at once with code 130.

If another engine holds the state folder, `beehave serve` exits with code 1 and says: "another beehave engine owns this state folder". If the launch fails, it also exits with code 1.

A build that differs from the build of the engine cannot join it. After an upgrade, restart `beehave serve`.

## Check the engine

Run `beehave status`. While the engine runs, the output has the line `engine: serve, pid <N>, since <time>`. Otherwise it has the line `no engine runs`. The lock on the state folder decides which line shows. The file `engine.info` only describes the engine.

## Turn on the review poll

The review poll is off while `agents.review_poll_minutes` is 0. Under `beehave serve`, set `agents.review_poll_minutes` to 60.

## Sample launchd file

Save the file as `~/Library/LaunchAgents/dev.beehave.serve.plist`. Then run `launchctl load ~/Library/LaunchAgents/dev.beehave.serve.plist`.

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>dev.beehave.serve</string>
  <key>ProgramArguments</key>
  <array>
    <string>/usr/local/bin/beehave</string>
    <string>serve</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>BEEHAVE_CONFIG</key>
    <string>/Users/you/.config/beehave</string>
    <key>BEEHAVE_STATE_DIR</key>
    <string>/Users/you/.local/state/beehave</string>
  </dict>
  <key>KeepAlive</key>
  <true/>
  <key>RunAtLoad</key>
  <true/>
  <key>StandardErrorPath</key>
  <string>/Users/you/Library/Logs/beehave-serve.log</string>
</dict>
</plist>
```

## Sample systemd unit

Save the file as `~/.config/systemd/user/beehave-serve.service`. Then run `systemctl --user enable --now beehave-serve`.

```ini
[Unit]
Description=beehave serve

[Service]
ExecStart=/usr/local/bin/beehave serve
Environment=BEEHAVE_CONFIG=%h/.config/beehave
Environment=BEEHAVE_STATE_DIR=%h/.local/state/beehave
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
```

`Restart=on-failure` does not restart the unit after `systemctl --user stop`, because the command exits with code 0 on SIGTERM. A second engine that finds the state folder busy exits with code 1, and the unit restarts it every 5 seconds until the first engine stops.
