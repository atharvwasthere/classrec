# classrec

Records a student's online classes (Zoom, Google Meet in Edge) with OBS on a Windows 11 laptop,
when she clicks "Record this class" on the card it shows for each class, and, in mode "full",
turns them into timestamped Hindi/Hinglish transcripts with Sarvam.
What it does and how to use it: README.md. Operations (install, updates, rollback, mode full, what it
changes in OBS): docs/OPERATIONS.md. What changed in each release: CHANGELOG.md.

## Building and testing

- `go build -o dist\classrec.exe ./cmd/classrec` for dev builds. The installed agent lives in
  `C:\classrec\` and is only ever a tagged release.
- `go test ./...` and `golangci-lint run ./...` (also with `GOOS=windows`) must pass; CI runs both.
- `classrec record-test` checks a change against the real OBS. Before touching OBS or the agent,
  make sure no class is on (no Zoom or Meet meeting window).
- Never commit `.env`, `.obs-password`, logs or recordings. Sarvam is called only in mode "full".
- Commit messages and PRs carry no AI attribution (no `Co-Authored-By` trailer).
- Every release is described in `CHANGELOG.md` before it is tagged; CI publishes that section as the
  release notes and refuses a tag without one.

## Code standards

Every change should remove code or concepts, never add layers.

- Errors flagged by the linters must be fixed from first principles, don't do mechanical moves to
  hide them. A `//nolint` needs a comment saying why.
- Linters (`.golangci.yml`): govet, staticcheck, unused, revive (exported, unexported-return),
  gocognit (max 20), funlen (80 lines / 50 statements), interfacebloat (max 6 methods), depguard.
- Dependencies point one way:
  - detect, obs, media and transcript import no other internal package except fsx (obs owns its
    `ws` transport and `obstest` fake);
  - recorder imports only obs, detect (types) and fsx;
  - pipeline never imports recorder, overlay or desktop;
  - only `cmd/classrec` wires everything together.
- Interfaces are small and defined where they are used (no more than 6 methods). A concrete type
  such as `*obs.Client` satisfies them; fakes implement only what tests use.
- One table per variation: class apps live in `detect.Apps` (exe, window class, meeting match,
  OBS scene). Add an app there, not with a new switch.
- Types with behaviour (Recorder, Machine, Client, Overlay) keep their fields unexported; plain
  data (Config, Target, JSON types) may export fields.
- Export only what another package uses; delete what nothing uses.
- One test per user-visible promise. No tests of timing, call order or locking.
