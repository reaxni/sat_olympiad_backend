# May 2026 V1 Module 2 import

The generated private bank is `private/may-2026/module2-bank.json`. It contains 27 Reading and Writing Module 2 questions and 22 Math Module 2 questions, with answer keys and 217 PNG assets. The scanned passages, equations, underlines, figures, graphs, and answer choices are preserved as cropped original images. Reading question stems are text. Math question 18's frequency tables are structured table blocks. OCR descriptions are included for the image blocks; the original scans determine the displayed content. Scanned passages and figures have zoom controls, and OCR descriptions stay available as alternative text without repeating them as visible captions.

The Reading section uses PDF pages 28–55, with the duplicate scans of question 13 on pages 40–41 combined into one complete question. The Math section uses pages 78–99. Keys come from pages 101 and 103. Three clear Reading key errors were corrected. Each private `source` object retains the printed key and correction reason. The private `source-manifest.json` retains reviewed keys, corrections, numeric equivalents, and the transcribed table data; these are not hardcoded in the public extraction program.

## Validate and import

From the backend directory:

```powershell
go run ./cmd/admin validate private/may-2026/module2-bank.json
$env:EXAM_TITLE = 'May 2026 V1 Module 2'
go run ./cmd/admin import private/may-2026/module2-bank.json may-2026-v1-module2
```

The optional last argument imports into a separate exam ID. It does not switch the active exam served by the API. To serve this bank, configure `EXAM_ID=may-2026-v1-module2`, `EXAM_TITLE`, and the entry schedule on the backend and restart it. A new exam requires `EXAM_OPEN_AT` and `EXAM_ENTRY_CLOSE_AT` in the import environment. Existing exam schedules are preserved. The CLI loads an optional local `.env`; Railway uses injected environment variables. `DATABASE_URL` must point to the intended reachable PostgreSQL database. The server must have initialized the base schema before an import.

The import validates all 49 question identities, keys, difficulty labels, and image references before writing. Questions and assets are inserted in one transaction; an exam with existing attempts cannot be changed. `private/` is ignored by Git and must not be copied to the website's public assets.

## PostgreSQL storage and API

Migration `004_question_assets.sql` adds `question_assets` with `question_id`, asset `id`, `mime_type`, and `data bytea`. It also adds private `questions.source_json` for source page and key provenance. Question image blocks use `url: "asset:ASSET_ID"`; the top-level bank `assets` array has `{ "id", "mimeType", "dataBase64" }`. The importer decodes base64 into binary PostgreSQL storage. Inline base64 images from older JSON banks are converted into references by the same importer. Structured graph and table blocks remain in `public_json` as JSONB.

On an authorized section response, the server loads only assets belonging to that section and resolves references to the image data URIs already supported by the website. Review assets and explanations are resolved only after the review release gate opens. There is no public image directory or ungated image endpoint. Existing embedded-image banks continue to render. Migration changes no saved scores or answer keys.

Banks are limited to 100 MiB and individual assets to 10 MiB. PNG and JPEG headers are checked against their declared MIME type. Missing or invalid images stop validation and import rather than dropping figures.

## Reproduce extraction

Use Python with `pypdf`, Pillow and NumPy, plus Windows OCR:

```powershell
python scripts/extract_may2026_module2.py --prepare 'C:\Users\Amir\Downloads\SAT May 2026 V1 Full.pdf'
powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/ocr-scans.ps1 -InputDir "$PWD\private\may-2026" -OutputPath "$PWD\private\may-2026\ocr.json"
python scripts/extract_may2026_module2.py
```

The reviewed private `source-manifest.json` is required alongside the OCR output. This extraction script is specific to the supplied 103-page scan. Other PDFs need their page and crop mappings reviewed before import. Both Module 2 sets use the existing hard difficulty label; per-question calibrated difficulty was not supplied by the PDF.
