# clippy

Cut short clips out of movies and shows in your Plex library, straight from
the command line. Point it at whatever is playing right now and say
"the last 20 seconds", or give it explicit timestamps.

```
$ clippy now
#  USER   PLAYER       TITLE                               POSITION           STATE    KEY
1  yacin  Living Room  Sintel (2010)                       0:05:00 / 0:14:48  paused   48213

$ clippy clip --last 20s --subs
Sintel (2010)  0:04:40 → 0:05:00 (20s)
subtitles: English (SRT), srt
Sintel_4m40s-5m00s.mp4
```

clippy is a single static Go binary. It needs `ffmpeg` on the machine that
runs it and network access to your Plex server. The media is read from the
server over HTTP: clippy runs a small localhost block cache that ffmpeg reads
from, fetching the file from Plex in 4 MB ranges, so only the part of the file
around the clip is transferred and ffmpeg's constant seeking never leaves the
machine. clippy can run anywhere, not just on the Plex host.

## Install

```
make install          # or: go install github.com/ynadji/clippy@latest
make dist             # static binaries for darwin/linux, amd64/arm64 in dist/
```

## Setup

```
clippy login
```

This prints a code for <https://plex.tv/link>, waits for you to enter it,
discovers your server and writes `~/.config/clippy/config.json`. If the account
has several servers, pass `--server <name>`; to skip discovery entirely use
`--url http://host:32400`.

You can also skip the file and set `CLIPPY_PLEX_URL` and `CLIPPY_PLEX_TOKEN`.

The config file looks like this; everything after `client_id` is optional:

```json
{
  "url": "http://192.168.1.10:32400",
  "token": "...",
  "client_id": "...",
  "server_name": "nas",
  "ffmpeg": "/usr/lib/plexmediaserver/Plex Transcoder",
  "output_dir": "/home/yacin/clips",
  "path_map": { "/data/media": "/Volumes/media" }
}
```

- `path_map` is an optional fast path. When an entry matches the server-side
  path and the file exists here, clippy reads it directly instead of streaming
  it from the server. Use it when clippy runs on the Plex host or on a box that
  mounts the same share. `--remote` ignores it for one run.
- `ffmpeg` overrides the binary. Burning in subtitles needs an ffmpeg built
  with libass; Homebrew's current `ffmpeg` formula is not, but Plex's own
  transcoder is, as are most Linux distro packages and
  [static builds](https://johnvansickle.com/ffmpeg/).

## Usage

```
clippy now                                   what is playing, with positions and keys
clippy search <query>                        find movies and episodes
clippy clip [flags]                          cut a clip
clippy clip --streams                        list audio/subtitle streams of the target
clippy config                                show the active configuration
clippy doctor                                check server connectivity, read throughput and ffmpeg
```

### Picking what to clip

With no source flag, `clip` uses the single active session. Otherwise:

| flag            | meaning                                            |
| --------------- | -------------------------------------------------- |
| `--session N`   | the Nth row from `clippy now`                      |
| `--user NAME`   | the session belonging to that Plex user            |
| `--key K`       | a library item by rating key (from `now`/`search`) |
| `--query TEXT`  | the first search match                             |

### Picking the range

| flags                           | meaning                                                   |
| ------------------------------- | --------------------------------------------------------- |
| `--last 20s`                    | the 20 seconds ending at the current playback position    |
| `--before 10s --after 5s`       | a window around the current position                      |
| `--start 12:34 --end 12:51`     | explicit range                                            |
| `--start 12:34 --duration 15`   | explicit start and length                                 |
| `--at 12:34`                    | pretend the current position is 12:34 (works with `--key`)|

Times accept `1:02:03`, `12:34`, `90`, `90.5` and Go durations like `1m30s`.

The current position is read live from the player through Plex Companion
when the player supports it (`clippy now` shows `player` in the VIA column).
Otherwise clippy falls back to the server's last progress report, which some
clients only send every few seconds or on pause, and says so. `--at` overrides
it either way.

### Output

By default the clip is re-encoded to H.264/AAC MP4, capped at 1080p, with the
audio track that is selected in Plex. That plays everywhere and makes the cut
frame-accurate. Files are named `<Show>_S01E02_<Episode_Title>_<start>-<end>.mp4`
or `<Movie>_<start>-<end>.mp4`.

| flag              | meaning                                                        |
| ----------------- | -------------------------------------------------------------- |
| `-o FILE`         | output path (default `<title>_<start>-<end>.mp4` in `output_dir`) |
| `--copy`          | stream copy instead of re-encoding: near-instant, but the cut lands on the previous keyframe and the result may not play on phones if the source is HEVC/DTS |
| `--subs`          | burn in the subtitle track selected in Plex (else the first)   |
| `--sub-stream N`  | burn in a specific subtitle stream (see `--streams`)           |
| `--audio N`       | use a specific audio stream                                    |
| `--max-height N`  | downscale cap (0 to keep source resolution)                    |
| `--crf N`, `--preset P` | x264 quality and speed                                   |
| `--remote`        | stream from the server even if `path_map` matches              |
| `--direct`        | let ffmpeg read the server URL itself, bypassing the local block cache (slow for mp4) |
| `--hwaccel X`     | hardware decoder: `auto` (VideoToolbox on macOS), `none`, `cuda`, `vaapi`, ... |
| `--dry-run`       | print the ffmpeg commands instead of running them (token masked) |

Bitmap subtitles (PGS, VobSub) cannot be burned in. HDR sources are not tone
mapped yet, so they will look washed out when re-encoded to 8-bit.

## Engines

`clippy clip` shells out to an ffmpeg binary by default. Built with
`make build-libav`, it also has a `--engine libav` mode that links FFmpeg's
libraries directly (via go-astiav) and uses VideoToolbox and AudioToolbox on
Apple platforms. That engine is what the Banana Clip iOS app embeds; the CLI
mode exists so it can be tested against a real server on a Mac. See
`third_party/build-ffmpeg.sh` for the LGPL-only FFmpeg build and
`docs/LICENSING.md` for why.

## Layout

- `internal/plex` – a small Plex Media Server and plex.tv client
- `internal/clip` – ffmpeg command construction and subtitle trimming
- `internal/proxy` – the block cache remote files are read through (in-process for libav, over localhost HTTP for the ffmpeg binary)
- `internal/clip/libav` – the linked-FFmpeg engine (build tag `libav`)
- `mobile` – the gomobile API the iOS app calls
- `third_party` – FFmpeg and subtitle-stack build scripts for macOS and iOS
- `internal/config` – config file handling
- `main.go` – the CLI

The core packages are deliberately free of CLI concerns so a later
`clippy serve` mode can expose the same operations over HTTP to an iOS app:
show the active session and its position, scrub a range, and request a clip.

## Development

```
make test
CLIPPY_FFMPEG=/path/to/full/ffmpeg go test ./internal/clip -run TestFFmpegPipeline -v
```

The ffmpeg integration test builds its own fixture and skips itself when the
available ffmpeg lacks libass.
