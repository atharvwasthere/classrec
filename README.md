# classrec

Records online classes (Zoom, and Google Meet in Microsoft Edge) on a Windows 11 laptop with OBS,
one click per class, and can turn a recording into a timestamped Hindi/Hinglish transcript with
[Sarvam](https://www.sarvam.ai/).

It is built for one student on one laptop: it starts at logon, stays out of the way, never records
without being asked, and files every class in its own folder.

- **One click.** When a class starts, a small card says *"Zoom class detected — Record this class"*.
  Nothing is recorded until she clicks it.
- **Stops by itself** 10 seconds after she leaves the call.
- **Records only the class:** the Zoom or Edge window and that app's sound. Her microphone, other
  apps and system sounds stay out.
- **Tells someone when something is wrong:** no class audio, no class video, on battery, or a
  stuttering recording, on the same card.
- **Updates itself** from GitHub Releases between classes, checks every update, and rolls back a
  bad one.
- **Transcripts are optional.** Mode `record` (the default) never calls Sarvam and costs nothing.

## How it works

```
 every 10 s                 she clicks                 10 s after she leaves
 ───────────►  in a call?  ───────────►  OBS records  ─────────────────────►  D:\Class Recordings\
              (see rules)   "Record this   the class                          <date> <day> - <name> (Zoom|Meet)\
                             class"        window + its sound                   part-01.mkv, pieces.json, class.log
                                                                              └─► transcript\ (mode full only)
```

The agent (`classrec agent --background`) runs from a logon task and polls the desktop every
10 seconds. It drives OBS over obs-websocket: its own OBS profile and scenes, a window capture and
an app-audio capture of the class window, recording into a staging folder that is filed when the
class ends.

### When is she in a class?

There is no official way to ask Zoom or Meet whether someone is in a call, so classrec uses the
signal each app itself shows. These rules were checked against real Zoom and Meet calls on the
target laptop.

| App | In the call | Left |
|---|---|---|
| **Meet** (Edge) | An Edge tab named `Meet - …` that Edge marks *"Microphone recording"* or *"Camera recording"*. Holds while she is muted with the camera off, or looking at another tab. | No such tab: the tab is closed, or she clicked **Leave call** (the tab stays, but Edge drops the mark). |
| **Zoom** | Zoom's meeting window is on screen, or its floating video window (meeting minimised), or the *"You are screen sharing"* toolbar. | None of those is on screen. Zoom running in the tray or on its Home screen is not a class. |

Edge's tab names are read with Windows UI Automation (the screen-reader interface), so every tab
counts, not only the one showing. Sound and the microphone play no part: a muted Zoom does not hold
the microphone, and a class's sound can go on after she has left.

### What gets recorded

The class window, exactly as it looks, scaled to 1280×720 at 15 fps, and only that app's sound.

| She… | The video shows |
|---|---|
| is in a Zoom meeting (normal or full screen) | the Zoom meeting window: faces, the teacher's shared screen, toolbars |
| minimises Zoom, or shares her own screen | Zoom's floating video window (Zoom hides the meeting window) |
| is in a Meet call | the Edge window, whichever tab is showing |

Sound is recorded throughout. If OBS stops or crashes mid-class it is restarted and the class
continues as `part-02.mkv`, and so on; `pieces.json` records when each piece began.

### Warnings on the card

| Card | Means | When |
|---|---|---|
| 🔴 **No class audio — call Adi** | the recording is silent | the class app has been quieter than the silence threshold for a minute |
| 🟠 **No class video — call Adi** | the recording has no picture | OBS has rendered no picture of the class window for a minute; the agent also points the capture at the window again |
| 🟠 **Plug in charger** | running on battery | |
| 🟠 **Recording is struggling** | the video stutters | OBS dropped a tenth of a minute's frames (Meet's usual ~2% does not count) |

## Install

Requirements: Windows 11, [OBS Studio](https://obsproject.com/) 30 or newer with its websocket
server enabled (Tools › WebSocket Server Settings, with a password), an ffmpeg build
(`ffmpeg.exe`, `ffprobe.exe`), and a `D:` drive for recordings.

1. Close OBS, and make sure no class is on.
2. Download `classrec.exe` and `classrec.exe.sha256` from the
   [latest release](https://github.com/atharvwasthere/classrec/releases/latest) into `C:\classrec\`
   and check the hash:
   ```powershell
   (Get-FileHash C:\classrec\classrec.exe).Hash; Get-Content C:\classrec\classrec.exe.sha256
   ```
3. Put `ffmpeg.exe`, `ffprobe.exe` and `.obs-password` (the websocket password, one line) in
   `C:\classrec\`. For transcripts, also `.env` containing `SARVAM_API_KEY=…`.
4. Install. It writes the OBS profile, registers the agent to start at logon, and runs the selftest;
   every line should say PASS:
   ```powershell
   C:\classrec\classrec.exe install
   ```
5. Start the agent now (or sign out and back in):
   ```powershell
   Start-ScheduledTask classrec
   ```

`classrec uninstall` removes the logon task; recordings, logs and the OBS profile stay.

## Everyday use

There is nothing to do except click **Record this class**. Classes land in
`D:\Class Recordings\<date> <day> - <name> (Zoom|Meet)\`.

| Command (from `C:\classrec\`) | Does |
|---|---|
| `.\classrec.exe selftest` | checks everything the agent relies on (OBS, overlay, task, disk); PASS/FAIL per line |
| `.\classrec.exe process "D:\Class Recordings\<class>"` | transcribes one class with Sarvam (resumable; never pays twice for the same audio) |
| `.\classrec.exe calibrate "<class>\part-01.mkv"` | sets the silence threshold from a real recording |
| `.\classrec.exe record-test --app zoom` | records 30 s of a Zoom or Meet window and checks sound, picture and files |
| `.\classrec.exe overlay-demo --state offer` | shows the card without a class, and writes PNGs of every state |

**Transcripts for every class:** put the Sarvam key in `C:\classrec\.env` and `{ "mode": "full" }`
in `C:\classrec\settings.json`, then restart the agent. Each recorded class gets
`transcript\transcript.md` a little after it is filed. Cost is about ₹0.58 per minute including GST.

## Files

| What | Where |
|---|---|
| Program, ffmpeg, `.obs-password`, `.env`, `settings.json` | `C:\classrec\` |
| Agent logs (kept two weeks), detect logs, selftest reports | `C:\classrec\logs\` |
| Classes | `D:\Class Recordings\<class>\` |
| Recordings in progress | `D:\Class Recordings\_staging\` |
| Reports for remote debugging (logs only, never recordings) | `D:\Class Recordings\_reports\` |

Detect logs keep only Zoom and Edge windows; every other window title is stripped before the log
is written.

## Troubleshooting

| Symptom | Look at |
|---|---|
| No card when a class starts | `C:\classrec\logs\agent-<date>.log` should say `class detected`. If not, the latest `detect-<date>.jsonl` shows what the agent saw (window classes, Edge tab names). |
| Recording has no picture or is black | the class log: `the class window is now …` lines show the capture following the window; `no picture` lines show the video check. `record-test` reproduces it. |
| A recording was not filed | files OBS still held at the end stay in `_staging` and are listed in `pending.json`; the agent files them at its next start. A recording left running by a killed agent is filed as *Recording left running*. |
| A new release misbehaves | roll back by hand: see [docs/OPERATIONS.md](docs/OPERATIONS.md#roll-back-by-hand). |

## Development

Go 1.27, Windows-native; everything but the desktop, overlay and OBS process code also builds and
tests on Linux.

```powershell
go build -o dist\classrec.exe ./cmd/classrec   # dev builds go in dist\, never C:\classrec
go test ./...
golangci-lint run ./...                         # and again with $env:GOOS='windows'
```

CI (`.github/workflows/ci.yml`) vets, lints and tests on Linux and Windows. Pushing a `v*` tag builds
`classrec.exe` with the version embedded, writes its sha256, and publishes a GitHub Release, which
installed agents pick up within a day. Code standards are in [CLAUDE.md](CLAUDE.md); operating the
installed agent (updates, rollback, what it changes in OBS) is in [docs/OPERATIONS.md](docs/OPERATIONS.md).

| Package | Role |
|---|---|
| `cmd/classrec` | the CLI and the agent loop: wires detection, recorder, overlay, warnings, logs, self-update |
| `internal/detect` | pure rules: a desktop sample → in a call or not → start/stop events; replays logs in tests |
| `internal/desktop` | Windows: top-level windows, Edge tab names (UI Automation), battery, keep-awake |
| `internal/recorder` | drives OBS for one class: profile, scene, capture, pieces, filing, crash recovery |
| `internal/obs` | obs-websocket v5 client, profile writer, and a fake OBS for tests |
| `internal/overlay` | the always-on-top card (Win32 + GDI+), never takes focus |
| `internal/pipeline`, `sarvam`, `transcript`, `media`, `chunk` | recording → audio → chunks → Sarvam → transcript, resumable |

[docs/phase0-results.md](docs/phase0-results.md) records the first transcription run that sized the
pipeline (accuracy spot checks, cost, and what went wrong).
