# classrec for Windows

Turns a recorded class into a timestamped transcript (Hindi / Hinglish).

## One-time setup

1. Put these three files in one folder, for example `C:\classrec\`:
   - `classrec.exe`
   - `ffmpeg.exe` and `ffprobe.exe` (from any Windows ffmpeg build)
2. In the same folder, copy `.env.example` to `.env` and replace `your_key_here` with your Sarvam API key.

## Run

```
C:\classrec\classrec.exe process "C:\Users\Admin\Class Recordings\GMT20251228-133527_Recording.mp4"
```

Keep the quotes when the path has spaces. The transcript is written to `out\<video name>\transcript.md`
in the folder you ran the command from.

## Good to know

- If it stops with an error and you run the same command again, it carries on from where it stopped and does not pay for the same audio twice.
- The recording date comes from a Zoom file name, else from the video's own creation date. If neither exists it stops immediately and asks for the date: add `--date 2026-02-08` to the command.
- Cost is about ₹0.58 per minute of class including GST (a 2 hour class is about ₹70).
- Windows may say "Windows protected your PC" the first time, because the program is not signed. Click "More info", then "Run anyway".

## Testing the class agent (not for daily use yet)

Two test commands. Nothing is recorded and no Sarvam credits are spent.

**Detection log, during one real class.** Before the class starts, open a Command Prompt and run:

```
C:\classrec\classrec.exe agent --debug-detect
```

Leave the window open (minimized is fine) for the whole class and a few minutes after it ends, then press Ctrl+C.
Every 10 s it writes all window titles and Zoom/Edge microphone state to `C:\classrec\logs\detect-<date>_<time>.jsonl`.
Send that file back. The window titles of everything open on the laptop are in it.

**Overlay.** Run `C:\classrec\classrec.exe overlay-demo` (add `--status amber` or `--status red` to see the warning
colours). The first line should say `per-monitor-v2 DPI aware: true`. Drag the pill to move it; the position is kept
in `C:\classrec\settings.json`. Each button click adds the seconds since start to `C:\classrec\overlay-demo\marks.json`.
Focus test: in a Zoom meeting, click the button, then press Alt+A; Zoom must still mute/unmute.
