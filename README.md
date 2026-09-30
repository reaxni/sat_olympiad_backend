# 1609 SAT Olympiad backend

Go + PostgreSQL API for the React frontend in `../SAT_website`. The backend owns password authentication, cookie sessions, the 49-question bank, answers, section deadlines, browser-event observations, grading, release gates, and leaderboard. No question key or unreleased explanation is sent to the frontend.

## Local setup

On Windows, the quickest route is to open PowerShell in this folder and run:

```powershell
powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\set_local.ps1
```

Alternatively, copy `.env.example` to an ignored `.env` in this folder, fill in the local values, and run `go run ./cmd/server` from this folder (or use a GoLand configuration with this folder as its working directory). The server and admin CLI now load `.env` automatically for local runs. Values already set in the process take precedence. On Railway, or when `APP_ENV=production` is already set in the process, `.env` is skipped and Railway's injected variables are used. A backend running on your computer needs a local PostgreSQL URL or Railway's `DATABASE_PUBLIC_URL`; `postgres.railway.internal` resolves only inside Railway. Keep `.env` out of Git.

`set_local.ps1` finds the installed PostgreSQL service, starts it if needed, creates the `sat_olympiad` database if missing, configures the development environment, and runs the Go server. It asks for your PostgreSQL password on the first run and saves it with Windows user-scoped encryption in ignored `.local/` files; later runs reuse it. The local auth secret and exam schedule are saved there too. Create an account with email and a password of 6–12 characters; no email code is sent. Use `-ResetCredentials` if your PostgreSQL password changes, or `-ResetSchedule` to open a new local entry window. For example: `powershell.exe -NoProfile -ExecutionPolicy Bypass -File .\set_local.ps1 -ResetSchedule`. The script does not affect Railway settings.

Gmail settings are no longer used by the active authentication route. Existing accounts created with email codes have no password; an organizer must migrate them before those students can sign in with the new form.

The manual setup below is useful for other operating systems or custom database configurations.

1. Create a PostgreSQL database and set `DATABASE_URL` in `.env` or your shell. Copy the variable names from `.env.example`. Set `AUTH_SECRET` to at least 32 random characters. Set `EXAM_OPEN_AT` and `EXAM_ENTRY_CLOSE_AT` in RFC 3339 UTC format. Set `APP_ENV=development`, `PORT=8080`, and `ALLOWED_ORIGINS` to the exact Vite origin you use.
2. The active `POST /auth/password/session` endpoint saves the email on sign-up and establishes a cookie session. Passwords are stored as bcrypt hashes. Email ownership is not verified.
3. Run `go run ./cmd/server` from this folder. It applies `migrations/001_init.sql` and starts the API. `GET /healthz` checks PostgreSQL.
4. Import an organizer-authored JSON bank with `go run ./cmd/admin import path/to/bank.json`. The import requires exactly 27 Reading and Writing and 22 Math questions and is refused once any attempt exists. The bank format is `{ "questions": [{ "public": Question, "correctAnswer": AnswerValue, "explanation": ContentBlock[] }] }`, using the types in `../SAT_website/src/domain/exam.ts`. Reading questions should put source material in `public.passages` and the task stem in `public.prompt`; optional `readingSkill` identifies inference, cross-text connections, rhetorical synthesis, and other item types. Content blocks support text with `marks` (start/end underline, italic, or bold ranges), `list`, `table`, `graph` (axis bounds, points, and optional lines), and `image`. Math graphs and images can be zoomed in the exam. Numeric keys may also include `"accepted": ["1/2", "0.5"]`. Keep this private file outside both public repositories. Question image blocks can use `asset:` references to the private bank assets array, or embedded base64 PNG, JPEG, or SVG URLs. The importer stores decoded images in PostgreSQL; they are only returned with the authorized current section. The bank import limit is 100 MiB and the individual image limit is 10 MiB.

See [question content format](docs/question-content.md) for original JSON examples of passages, task stems, underlines, lists, tables, and graphs.
5. The frontend already has `.env.development.local` pointing at `http://127.0.0.1:8080`. Restart Vite after changing environment files. Use `http://127.0.0.1` for both apps locally; if you use `localhost` for the frontend, change the API URL to `http://localhost:8080` so the cookie stays same-site.

Without the private question bank, entry is blocked with a clear message. This repository contains no real questions, keys, or sample email credentials. The frontend's development mock remains available by temporarily removing its `VITE_API_BASE_URL` setting.

## Exam behavior

### Converting and importing the supplied hard sets

The two supplied JSON files use a different shape from the website. Convert them locally with:

```powershell
go run ./cmd/admin convert 'C:\Users\Amir\Downloads\gemini-code-1790520745691.json' 'C:\Users\Amir\Downloads\gemini-code-1790520468521.json' 'private\exam-bank.json'
```

The generated `private/exam-bank.json` has 49 website-compatible questions. Each item has a `public` question, a separate private `correctAnswer`, an `explanation` array, and a `difficulty` of `easy`, `medium`, or `hard`. `private/` is ignored by Git; never put answer keys in the website's `public/` directory. The source labels both sets hard, so all converted items are marked hard. The source `points` values are not used because they describe near-uniform allocations, not difficulty ratings. Math question 16's key is corrected from C to D because the given side ratio, equal sides, and included angle already prove congruence by SAS. Math question 20 is rewritten with explicit 47% and 62% survey values because the source omitted the data needed to answer it. Add explanations and independently review the questions and keys before a live exam.

Start the server once to create the exam row and apply migrations. Then set `DATABASE_URL` to the target PostgreSQL connection string and run `go run ./cmd/admin import private/exam-bank.json`. For Railway, run this organizer command from a trusted machine with a reachable Railway PostgreSQL connection string, or run `/admin import /path/to/exam-bank.json` inside a private Railway one-off shell after supplying the bank there. The import replaces the bank atomically and refuses to run after an attempt exists. Do not put the bank or database credentials in a public repository or deploy them in the frontend.

Scoring uses difficulty weights of 1 (easy), 2 (medium), and 3 (hard). For each section, the backend calculates `200 + 10 × round(60 × earnedWeight / availableWeight)`, yielding 200–800 per section and 400–1600 overall. This is an independent Olympiad scoring rule, not College Board's official SAT scoring model. Since the supplied questions are all marked hard, they currently have equal weight; mix verified difficulty labels to make individual questions contribute differently.

- The server starts Reading and Writing (27 questions, 32 minutes), then switches to Math (22 questions, 35 minutes) immediately on submission or deadline. A background deadline worker advances attempts even if the browser closes. The browser can reload and restore the authoritative attempt and saved answers.
- Answer saves use revisions and mutation IDs. Browser events use event IDs and group related focus/full-screen signals. At five counted events, the API restricts the attempt and blocks further answers. The organizer can also lock an attempt with `go run ./cmd/admin lock ATTEMPT_ID "reason"`.
- Each section score is on an **independent Olympiad** 200–800 scale: `200 + 10 × round(60 × correct / questionCount)`. The total is 400–1600. This is not an official SAT score. Numeric fractions and decimals are compared exactly as rational numbers. Tied totals rank by shorter time.
- `EXAM_ENTRY_CLOSE_AT` is the shared final deadline as well as the last entry time. At that instant, both sections stop, started attempts are completed and scored using saved answers, and the leaderboard releases automatically. The worker runs at startup and every second, including for absent browsers. A leaderboard request reconciles unfinished attempts before returning ranks. Never-started and disqualified attempts are excluded. Answers received after the deadline are rejected; unsent offline drafts cannot be counted.
- Results are personal when the test is complete. Correct answers and explanations stay locked until `go run ./cmd/admin release explanations`. Ranking remains hidden before the shared deadline, even if an older manual release timestamp exists.
- The browser submits no IP field. The server uses its connection's `RemoteAddr` only; no IP eligibility restriction is configured because no policy threshold was supplied. If Railway proxy headers are used for a future IP policy, accept them only from configured trusted proxies.

## Railway

For scanned exam banks, see [PDF Module 2 import and image storage](docs/pdf-exam-import.md). Images are stored as PostgreSQL binary assets; the bank JSON can reference them with `asset:` URLs. The organizer import limit is now 100 MiB, with a 10 MiB limit per image. Use `admin validate` to check a bank without connecting to PostgreSQL, and an optional final exam ID with `admin import` to store a separate exam.

Push this folder to a GitHub repository and create a Railway service from it. Railway detects the root `Dockerfile` and reads `railway.json`; the service listens on Railway's `PORT` and exposes `/healthz`. Add a Railway PostgreSQL service and set backend `DATABASE_URL` to the reference `${{Postgres.DATABASE_URL}}` (adjust `Postgres` if you renamed that service). Set `APP_ENV=production`, `AUTH_SECRET`, exact `ALLOWED_ORIGINS`, `EXAM_OPEN_AT`, and `EXAM_ENTRY_CLOSE_AT` in Railway. SMTP variables are no longer needed. Set a public domain for the API.

For production, build the Vite frontend with `VITE_API_BASE_URL=https://YOUR_API_DOMAIN`. This value is compiled into the browser bundle; it is not a secret. Use a frontend and API under the same site (for example `exam.example.com` and `api.example.com`) with `COOKIE_SAMESITE=lax`. If they must be on different sites, set `COOKIE_SAMESITE=none` and use HTTPS; browsers may still block third-party cookies, so a same-site setup is preferred. `ALLOWED_ORIGINS` must list the exact frontend origin, never `*`.

The backend migration runs on start and uses `CREATE TABLE IF NOT EXISTS`. Make a PostgreSQL backup before schema changes. The organizer CLI needs a database connection to import the private bank and release results; do not expose the CLI as a public endpoint.

## GitHub release

The [GitHub repository](https://github.com/reaxni/sat_olympiad_backend) is configured as `origin`. The tag-triggered workflow at `.github/workflows/release.yml` runs tests and publishes server/admin archives for Linux, Windows, and macOS. Tag a version and push the tag to create its release. The `v0.1.0` tag has been pushed; check GitHub Actions for its artifact publishing status.

## Verification

Run `go test ./...`, `go vet ./...`, and `go build ./cmd/server`. With a configured PostgreSQL instance, exercise signup, password sign-in, start, saves, section submission, deadline recovery, release gates, and leaderboard through the frontend. See the detailed route contract in `../SAT_website/docs/go-api-contract.md`.

For deadline integration checks, set `TEST_DATABASE_URL` to a test PostgreSQL connection and run `go test ./internal/server -run TestExamClosePostgres -v`. The test creates and removes an isolated schema. It checks partial scoring, absent-browser recovery, rejected late saves, ranking order, and protected answer keys.
