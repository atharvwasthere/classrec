# Operating classrec

classrec runs on the student's Windows 11 laptop. It notices a class (a Zoom meeting window, or a
Google Meet tab in Edge) and shows a small card with a **Record this class** button. Nothing is
recorded unless she clicks it; if she does, it records with OBS until the class ends and files it in
`D:\Class Recordings\<date> <day> - <title> (<Zoom|Meet>)\`. In mode `full` it also
makes a Hindi/Hinglish transcript with Sarvam.

The installed agent lives in `C:\classrec\` and is always a tagged release, never a dev build.

## What is where

| What | Where |
|---|---|
| Agent, ffmpeg, ffprobe, `.env` (Sarvam key), `.obs-password`, `settings.json` | `C:\classrec\` |
| Agent logs (two weeks), detect logs, selftest reports | `C:\classrec\logs\` |
| Classes: `part-01.mkv`…, `pieces.json`, `class.log`, `transcript\` | `D:\Class Recordings\<class>\` |
| Recordings in progress, and pieces OBS still held at the end of a class | `…\Class Recordings\_staging\` (`pending.json` lists the held ones; they are filed at the next start) |
| Reports for remote debugging | `D:\Class Recordings\_reports\` |

Reports are selftest results, per-class logs and detect logs. Detect logs keep only Zoom and Edge
windows; every other window title is stripped before the log is written. Reports never contain
recordings or transcripts.

## Everyday commands (from `C:\classrec\`)

```powershell
.\classrec.exe selftest            # PASS/FAIL per check; writes logs\selftest-<time>.txt
.\classrec.exe process "D:\Class Recordings\<class>"   # transcript for one class (calls Sarvam)
.\classrec.exe calibrate "<class>\part-01.mkv"                     # set the silence threshold from a real class
.\classrec.exe overlay-demo --state red                            # see the pill without a class
```

## Fix a bug from Claude Code on the web

1. **Read the report.** Open the latest files in `D:\Class Recordings\_reports\`: `selftest-*.txt`,
   `class <class>.log` and `detect-<date>.jsonl`. A detect log replays in tests: copy it to
   `internal/detect/testdata/` and replay it with `readJSONL` and `replay`, as
   `TestReplayIgnoresClassSound` does, to reproduce the bug.
2. **Fix it** in a cloud session on `atharvwasthere/classrec`. CI (vet, lint, tests on Linux and
   Windows) must pass.
3. **Tag a release:** `git tag v0.1.1 && git push origin v0.1.1`. CI builds `classrec.exe` and
   `classrec.exe.sha256` and publishes a GitHub Release.
4. **The laptop updates itself within a day.** Once a day, when no class is on, the agent fetches
   the latest release, checks its sha256, keeps the running exe as `classrec.prev.exe`, and runs
   the new exe's selftest. If the selftest fails it puts the old exe back (the new one stays as
   `classrec.bad.exe`); if it passes, the agent queues a fresh run of its scheduled task and exits,
   and Task Scheduler starts it again on the new exe straight away. The update shows in `logs\agent-<date>.log` ("updated from … to …").

## Roll back by hand

If a release misbehaves in a way the selftest missed:

```powershell
Stop-ScheduledTask classrec
cd C:\classrec
Rename-Item classrec.exe classrec.bad.exe
Rename-Item classrec.prev.exe classrec.exe
Start-ScheduledTask classrec
```

It will update again the next day, so also publish a fixed release (or delete the bad release on
GitHub so "latest" is the good one).

## Switch to mode full (transcripts)

Mode `record` is the default and never calls Sarvam. For transcripts after every class, put the
Sarvam key in `C:\classrec\.env` (`SARVAM_API_KEY=…`) and set the mode in `C:\classrec\settings.json`:

```json
{ "mode": "full" }
```

The agent logs its mode when it starts and with every class; restart it to pick up the change
(`Stop-ScheduledTask classrec; Start-ScheduledTask classrec`). Transcription runs after the class
is filed; the transcript lands in `<class>\transcript\transcript.md`. Sarvam costs about ₹0.58 per minute of class including GST. In mode `record`,
transcribe a single class by hand with `classrec process "<class folder>"`.

## What the agent changes in OBS, and how to undo it

`classrec install` (with OBS closed) writes an OBS profile named **classrec**:
`%APPDATA%\obs-studio\basic\profiles\classrec\` with MKV, QuickSync H.264, CBR 1000 kbps,
2 s keyframes, the screen as canvas, 1280×720 at 15 fps, recording into `Class Recordings\_staging`.

During a class she chose to record, the agent:

- switches OBS to the classrec profile;
- creates and selects the scenes **classrec - Meet** and **classrec - Zoom** (a window capture and
  an app-audio capture of the class window, scaled to fit);
- **mutes Desktop Audio and Mic/Aux**, so room noise and echo stay out of the recording;
- starts OBS itself, minimised to the tray, if it is not running (`--disable-shutdown-check`
  `--disable-updater`, so no dialog waits for an answer mid-class).

To use OBS by hand afterwards: pick your own profile under **Profile**, your own scene, and unmute
Desktop Audio and Mic/Aux in the Audio Mixer. To remove classrec from OBS completely, close OBS and
delete the `profiles\classrec` folder; in OBS delete the two `classrec - …` scenes.

## Install

1. Close OBS, and make sure no class is on.
2. Put the release's `classrec.exe` in `C:\classrec\` and check it against `classrec.exe.sha256`:
   `(Get-FileHash C:\classrec\classrec.exe).Hash` must equal the hash in the file (case aside).
3. Copy `ffmpeg.exe` and `ffprobe.exe` (an ffmpeg essentials build) and `.obs-password` (the OBS
   websocket password, one line) into `C:\classrec\`. For mode full also `.env` with `SARVAM_API_KEY=…`.
4. Run `C:\classrec\classrec.exe install`. It writes the OBS profile, registers the agent to start
   at logon as her, and runs the selftest (which starts OBS and records 10 s into a scratch folder).
   Every line should say PASS.
5. Start the agent now with `Start-ScheduledTask classrec` (or sign out and back in).

## Uninstall

`C:\classrec\classrec.exe uninstall` removes the logon task. Recordings, logs and the OBS profile
stay; delete `C:\classrec\` by hand if wanted.
