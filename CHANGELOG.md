# Changelog

What changed in each release of classrec, newest first. The release notes on GitHub are taken from
this file, so every version is listed here before it is tagged.

## [Unreleased]

## [v0.1.12] - 2026-11-28

### Changed
- **Class files have readable names.** The video is named after the class, like its folder
  (`2026-11-28 Friday - Maths (Zoom).mkv`), so a copy taken out of the folder still says which class
  it is. Only after OBS was restarted mid-class is it split into `… - part 1.mkv`, `… - part 2.mkv`.
  The class log is `Recording log.txt` and the list of parts is `Recording parts.json` (were
  `part-01.mkv`, `class.log` and `pieces.json`). Classes recorded earlier keep their names and can
  still be transcribed.

## [v0.1.11] - 2026-11-28

### Added
- Settings in `settings.json`: `recordings_dir` (where classes are saved) and `help_contact` (who the
  card names when something needs fixing).
- A README and this changelog. Each GitHub release now shows its section from here, and a release
  without one fails to publish.

### Changed
- **Classes are saved to `Videos\Class Recordings` by default.** Set `recordings_dir` to keep using
  another folder, such as `D:\Class Recordings`.
- The card's "No class audio" and "No class video" warnings end with "call <help_contact>", or
  "ask for help" when none is set.
- CI uses the current GitHub Actions (`checkout@v5`, `setup-go@v6`), ahead of Node 20's retirement.

## [v0.1.10] - 2026-11-27

### Fixed
- **"Recording is struggling" no longer shows all the time.** It turned amber on every single
  dropped frame. It now shows only when a tenth of a minute's frames are dropped, which is when the
  video visibly stutters. A Meet call can cost OBS about 2% of frames on a low-end laptop (a repeated frame
  every few seconds), which nobody sees. The class log notes the minute's drops once instead of a
  line per frame.
- `record-test` checked the picture 5 seconds in, before OBS's capture had started, and reported no
  picture. It now checks at 15 seconds and says why when it finds none.

## [v0.1.9] - 2026-11-27

### Added
- **"No class video" warning.** Every 30 seconds of recording the agent asks OBS for its picture
  of the class. After a minute with no picture (OBS lost the window, or the frame is a single
  colour) it points the capture at the window again and the card turns amber until the picture is
  back. A white slide is not mistaken for this: a real Zoom or Edge window always has toolbars.
- `record-test` checks the picture as well as the sound and files.

## [v0.1.8] - 2026-11-27

### Changed
- **A recording stops 10 seconds after the student leaves the call** (was 5 minutes), and only leaving
  counts:
  - **Meet:** in the call while an Edge tab named `Meet - …` is marked by Edge as using the
    microphone or camera. Muted, camera off, or another tab showing still count as in the call.
    "You left the meeting" or closing the tab ends it.
  - **Zoom:** in the call while its meeting window, its floating video window (meeting minimised)
    or its screen-sharing toolbar is on screen. Zoom left running in the tray does not count.
  - Sound and the microphone no longer decide anything: a muted Zoom does not hold the microphone,
    and a class's sound can go on after they have left.
- A class is named after its window when **Record this class** is clicked, and keeps that name.

### Fixed
- **Meet recorded a black picture with no sound** once the student joined the call: Meet renames the Edge
  window when it moves from the lobby into the call, and OBS only finds an Edge window by its exact
  title. The capture now follows the class window whenever its title changes, including when they
  switch tabs.
- A Meet class folder was named after whichever tab was showing at the end, and included Edge's
  "and 2 more pages".
- Stopping the agent mid-class (`Stop-ScheduledTask`) left OBS recording into staging for ever. The
  agent now stops such a recording and files it as *Recording left running* when it starts again. A
  recording someone started in OBS by hand, into its own folder, is never touched.

### Removed
- Reading Windows' microphone-use record, and the selftest check for it.

## [v0.1.7] - 2026-11-27

### Fixed
- **Zoom recordings had a blank white area where the video and shared screen should be**; only
  Zoom's toolbars were recorded. OBS's "Automatic" capture picked an old method that cannot see
  what Zoom draws with the graphics card. Class windows are now captured with OBS's Windows 10
  method, which records the window exactly as it looks on screen.

## [v0.1.6] - 2026-11-26

### Changed
- The **Record this class** card goes away as soon as the meeting window closes, and comes back if
  they rejoin before the class ends.

### Fixed
- **A Meet class was announced as "Zoom class detected"** after a Zoom meeting: Zoom keeps its
  meeting window, hidden, in the tray, and that hidden window counted as a meeting. Only windows on
  screen count now.
- A flaky test in the fake OBS used by the test suite.

## [v0.1.5] - 2026-11-26

### Removed
- The **"I didn't get this"** button, and the marks it saved. Nothing showed the marks when only
  recording, which is how classrec is used. The recording card is now a single status line.

## [v0.1.4] - 2026-11-26

### Changed
- Reports for remote debugging go to `_reports` in the recordings folder, never OneDrive.

## [v0.1.3] - 2026-11-26

### Changed
- Classes are recorded to `D:\Class Recordings`, off the system drive. (From the next release the folder is a setting.)

## [v0.1.2] - 2026-11-26

### Changed
- **Nothing records without asking.** When a class is detected the card shows **Record this class**;
  the class is recorded only if the student clicks it.

### Fixed
- After updating itself the agent exited and never came back: Task Scheduler does not restart a
  task that exits with an error code. The agent now queues a fresh run of its task before exiting,
  so it restarts on the new version at once. Needs `classrec install` once to take effect.

## [v0.1.1] - 2026-11-26

### Fixed
- The selftest reported "the task runs as someone else" because Task Scheduler names the user by
  their security ID rather than by name.

## [v0.1.0] - 2026-11-26

First release.

### Added
- **The agent:** starts at logon, notices a Zoom meeting or a Google Meet call in Edge, records it
  with OBS, and files it in its own folder with `pieces.json` and a class log.
- **Recording with OBS:** its own OBS profile (MKV, QuickSync H.264, 1280×720 at 15 fps, 1000 kbps),
  a scene per app with a window capture and an app-audio capture, desktop and microphone audio
  muted. If OBS crashes mid-class it is restarted and the class continues as a second piece.
  Recordings are never deleted or overwritten; a file OBS still holds is filed at the next start.
- **The card:** a Windows 11 style always-on-top card that never takes focus from the class, with
  warnings: no class audio (red), on battery or OBS struggling (amber).
- **Transcripts (mode `full`):** joins the pieces, extracts and loudness-normalises the audio, cuts
  it at silences, transcribes it with Sarvam (`saaras:v4`, Hinglish) and writes a timestamped
  `transcript.md`. Resumable, and never pays twice for the same audio. Mode `record`, the default,
  never calls Sarvam.
- **Self-update:** once a day between classes it fetches the latest release, checks its sha256, runs
  the new version's selftest, and rolls back if that fails.
- `classrec install`, `uninstall`, `selftest`, `calibrate`, `process`, `record-test` and
  `overlay-demo`; reports for remote debugging, with every window other than Zoom and Edge stripped
  from the detect logs.

[Unreleased]: https://github.com/atharvwasthere/classrec/compare/v0.1.12...HEAD
[v0.1.12]: https://github.com/atharvwasthere/classrec/compare/v0.1.11...v0.1.12
[v0.1.11]: https://github.com/atharvwasthere/classrec/compare/v0.1.10...v0.1.11
[v0.1.10]: https://github.com/atharvwasthere/classrec/compare/v0.1.9...v0.1.10
[v0.1.9]: https://github.com/atharvwasthere/classrec/compare/v0.1.8...v0.1.9
[v0.1.8]: https://github.com/atharvwasthere/classrec/compare/v0.1.7...v0.1.8
[v0.1.7]: https://github.com/atharvwasthere/classrec/compare/v0.1.6...v0.1.7
[v0.1.6]: https://github.com/atharvwasthere/classrec/compare/v0.1.5...v0.1.6
[v0.1.5]: https://github.com/atharvwasthere/classrec/compare/v0.1.4...v0.1.5
[v0.1.4]: https://github.com/atharvwasthere/classrec/compare/v0.1.3...v0.1.4
[v0.1.3]: https://github.com/atharvwasthere/classrec/compare/v0.1.2...v0.1.3
[v0.1.2]: https://github.com/atharvwasthere/classrec/compare/v0.1.1...v0.1.2
[v0.1.1]: https://github.com/atharvwasthere/classrec/compare/v0.1.0...v0.1.1
[v0.1.0]: https://github.com/atharvwasthere/classrec/releases/tag/v0.1.0
