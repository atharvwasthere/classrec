# classrec phase 0 results

Class: `GMT20251228-133527_Recording_640x360.mp4`, 2h05m27s. Model `saaras:v4`, mode `codemix`, language `hi-IN`, no keyterms.
Outputs are in `out/GMT20251228-133527_Recording_640x360/` (gitignored): `transcript.md`, `segments.json`, `cost.json`, `h1-terms.md`, `manifest.json`, `raw/`.

## H3: five lines to check against the video

Timestamps are where Sarvam says each phrase starts. Chunk 2 starts at 00:58:04 and chunk 3 at 01:57:55, so these cover the offset math.

| # | Time | Chunk | Text |
|---|---|---|---|
| 1 | 00:00:03 | 1 | हाँ, कहिए आप लोग क्या कहना चाहते हैं? कैसा practice हो रहा है? … |
| 2 | 00:58:24 | 2 (first lines after the cut) | तो sir और एक question था मेरे पाँव में … around 28 years से corn है। |
| 3 | 01:14:04 | 2 | number 9 अगर हम white color से करें तो? |
| 4 | 01:43:10 | 2 | 5 second के लिए अचानक vertigo का attack होता है। … Yang brain में yellow dot डालिए। |
| 5 | 01:58:10 | 3 (first lines after the cut) | मीनाक्षी जी आप है ना ये सब जो protocols हैं ना … |

Each entry is a phrase of about 15 s (498 phrases in total), so the speech in the video should start within a few seconds of the stamp.

## H5: cost

125.47 min sent, billed per second and rounded up per chunk (7528 s).
₹61.48 + 18% GST ₹11.07 = **₹72.55**, at the ₹0.49/min list price. This is an estimate, so check the Sarvam dashboard.

The first attempt at chunks 1 and 2 failed (see below). If Sarvam billed those two failed attempts, the dashboard will show roughly ₹39 more. Sarvam's API does not say.

API wall time (`cost.json`): Sarvam runs all chunks of a job together, so the job time is shared.
- Job 1 (chunk 0): upload 4.1 s, job 26.0 s, download 0.3 s.
- Job 2 (chunks 1 and 2): upload 4.4 s and 0.4 s, job 25.7 s, download 0.3 s and 0.1 s.

## H1 prep: jyotish terms

This recording is about color-point therapy, not jyotish. **Only 4 of 498 lines match.** The full list is in `h1-terms.md`.

- `गुरु`: 3 lines, all the word guru/gurudev/guruji used in passing (00:47:40, 01:57:56, 02:00:20).
- `भाव`: 1 line, a false hit inside `संभावना` (00:27:32).
- Every other term, Devanagari or romanized, has 0 hits.

So this class cannot say anything about Sanskrit/jyotish accuracy. H1 needs a real jyotish class.

## Run twice: zero API calls

- Plain re-run: every stage skipped.
- With `raw/` moved aside and no key anywhere (`SARVAM_API_KEY` unset, env file missing): the transcribe stage reran and answered from the hash cache ("all 3 chunks already transcribed (cache hit), no API call"). The rebuilt `raw/` was byte-identical to the original. Any API call would have failed for lack of a key.
- The same checks run in the tests against a fake Sarvam server that counts requests.

## Things that went wrong

1. **My bug: stream-copied chunks were invalid.** Chunks 1 and 2 were cut with `-c copy -page_duration`, which writes negative Ogg granule positions. ffprobe still printed a duration, so my check passed, but Sarvam rejected the files ("Audio duration is 0", "list index out of range"). Fix: chunks are re-encoded (mono opus 32k), the check now fails on any ffprobe complaint, and there is a regression test. Chunk 0 was valid and already paid for, so the chunk stage keeps valid chunks and recuts only broken ones.
2. **Docs vs live API.** The docs show `timestamps.chunks`. The live response uses `timestamps.words`. The parser reads both.
3. **Other doc gaps.** The model is now `saaras:v4`. The presigned upload headers (`x-ms-blob-type: BlockBlob`) are not in the docs; they come from Sarvam's Python SDK.
4. **Audio reuse.** `~/Downloads/classrec-test/audio.ogg` was adopted because its duration matches the video within 1.5 s. A byte hash can't match a fresh extract.
5. **Embedded newlines.** 283 of 498 phrases contain newlines (several sentences). `transcript.md` turns those into spaces to keep one line per phrase. `segments.json` and `raw/` keep the originals.
6. **`deslop-please`** is a Rust-only linter (it reads `cargo metadata`). It cannot analyze this Go code and was not used.
