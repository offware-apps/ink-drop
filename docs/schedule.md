# Running feeds on a schedule

Nothing runs on its own: feeds are fetched on a schedule only once the user
enables a timer. The timer below runs `ink-feed` over every feed listed in
`~/.config/ink-drop/feeds.txt`, one feed or site address per line, and packs
each digest into `~/Books`. A feed with nothing new since the last run adds
nothing. The timer and cron search their own `PATH`, not the shell's: install
both stages there, or write their full paths. `systemctl --user
show-environment` prints the timer's `PATH`.

## TL;DR

```sh
# 1. List the feeds, one URL per line
mkdir -p ~/.config/ink-drop
echo https://go.dev/blog/feed.atom >> ~/.config/ink-drop/feeds.txt
# 2. Turn on the hourly run
systemctl --user enable --now ink-drop-feeds.timer
```

## systemd

`~/.config/systemd/user/ink-drop-feeds.service`:

```ini
[Unit]
Description=Fetch ink-drop feeds

[Service]
Type=oneshot
ExecStart=/bin/sh -c 'ink-feed < %h/.config/ink-drop/feeds.txt | ink-epub -to %h/Books'
```

`~/.config/systemd/user/ink-drop-feeds.timer`:

```ini
[Unit]
Description=Fetch ink-drop feeds hourly

[Timer]
OnCalendar=hourly
Persistent=true

[Install]
WantedBy=timers.target
```

`Persistent=true` makes up a run missed while the timer was stopped, the machine
off or the user logged out.

## cron

The same run from `crontab -e`:

```
0 * * * * ink-feed < $HOME/.config/ink-drop/feeds.txt | ink-epub -to $HOME/Books
```
