# classrec

Records online classes on Windows — Zoom, and Google Meet in Microsoft Edge — with one click, and
can turn a recording into a timestamped Hindi/Hinglish transcript with [Sarvam](https://www.sarvam.ai/).

When a class starts, a small card offers **Record this class**. Nothing is recorded until the student
clicks it, and recording stops by itself 10 seconds after they leave the call. Each class is saved
in its own folder with only the class window and its sound.

## Features

- **One click per class**, no OBS knowledge needed.
- **Knows when the call ends** — not when the window is minimised, the tab changes or the mic is muted.
- **Records only the class**: the Zoom or Edge window and that app's audio; no microphone, other apps
  or system sounds.
- **Warns on the card** when the recording has no sound or picture, is stuttering, or the laptop is
  on battery.
- **Survives crashes**: if OBS stops mid-class it is restarted and the class continues in a second part.
- **Updates itself** from GitHub Releases between classes, verifies each update and rolls back a bad one.
- **Optional transcripts** — the default mode only records and never calls Sarvam.

## Requirements

- Windows 10 or 11
- [OBS Studio](https://obsproject.com/) 30+, with its WebSocket server enabled and a password set
- [ffmpeg](https://www.gyan.dev/ffmpeg/builds/) (`ffmpeg.exe` and `ffprobe.exe`)
- For transcripts: a Sarvam API key

## Install

1. Close OBS. Download `classrec.exe` from the
   [latest release](https://github.com/atharvwasthere/classrec/releases/latest) into `C:\classrec\`,
   next to `ffmpeg.exe`, `ffprobe.exe`, and a file `.obs-password` holding the WebSocket password.
2. Run `C:\classrec\classrec.exe install`. It sets up an OBS profile, starts classrec at every logon,
   and runs a self-test; every line should say PASS.
3. Start it now with `Start-ScheduledTask classrec`, or sign out and back in.

## Settings

Optional, in `C:\classrec\settings.json`:

```json
{
  "recordings_dir": "D:\\Class Recordings",
  "help_contact": "Sam",
  "mode": "full"
}
```

| Key | Default | Meaning |
|---|---|---|
| `recordings_dir` | `Videos\Class Recordings` | where classes are saved |
| `help_contact` | none | who the card names when something needs fixing ("call Sam" instead of "ask for help") |
| `mode` | `record` | `full` also transcribes each class (needs `SARVAM_API_KEY=…` in `C:\classrec\.env`; about ₹0.58 per minute) |

Restart the agent after changing them.

## How it decides a class is on

| | In the call | Left |
|---|---|---|
| **Meet** | an Edge tab named `Meet - …` that Edge marks as using the microphone or camera — even muted, camera off, or on another tab | the tab is closed, or *Leave call* (Edge drops the mark) |
| **Zoom** | the meeting window, the floating video window (minimised) or the screen-sharing toolbar is on screen | none of these; Zoom in the tray does not count |

Edge's tabs are read with Windows UI Automation, so every tab counts, not only the visible one.

## Commands

| | |
|---|---|
| `classrec selftest` | check OBS, the card, the logon task and disk space |
| `classrec process <class folder>` | transcribe one class (resumable, never pays twice) |
| `classrec record-test --app zoom` | 30-second test recording of a Zoom or Meet window |
| `classrec calibrate <recording>` | set the silence threshold from a real class |
| `classrec uninstall` | stop starting at logon (recordings are kept) |

Logs are in `C:\classrec\logs\`. Operating details — updates, rollback, what classrec changes in
OBS — are in [docs/OPERATIONS.md](docs/OPERATIONS.md); release history is in [CHANGELOG.md](CHANGELOG.md).

## Development

```powershell
go build -o dist\classrec.exe ./cmd/classrec
go test ./...
golangci-lint run ./...
```

CI vets, lints and tests on Linux and Windows. Pushing a `v*` tag builds `classrec.exe`, publishes it
with its sha256 and the version's [CHANGELOG](CHANGELOG.md) section, and installed copies pick it up
within a day. Code standards are in [CLAUDE.md](CLAUDE.md).
