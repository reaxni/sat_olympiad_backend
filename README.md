# 1609 SAT Olympiad backend

Go + PostgreSQL API for the React frontend in `../SAT_website`. The backend owns email verification, cookie sessions, the 49-question bank, answers, section deadlines, browser-event observations, grading, release gates, and leaderboard. No question key or unreleased explanation is sent to the frontend.

## Local setup

1. Create a PostgreSQL database and set `DATABASE_URL` in your shell. Copy the variable names from `.env.example`; the Go program reads process environment variables (it does not parse `.env` files). Set `AUTH_SECRET` to at least 32 random characters. Set `EXAM_OPEN_AT` and `EXAM_ENTRY_CLOSE_AT` in RFC 3339 UTC format. Set `APP_ENV=development`, `PORT=8080`, and `ALLOWED_ORIGINS` to the exact Vite origin you use.
2. Configure SMTP (`SMTP_HOST`, `SMTP_PORT`, `SMTP_USER`, `SMTP_PASSWORD`, `SMTP_FROM`) for real email verification. For local testing only, set `DEV_EMAIL_LOG=true` to print a code in the backend terminal. Never enable this in production.
3. Run `go run ./cmd/server` from this folder. It applies `migrations/001_init.sql` and starts the API. `GET /healthz` checks PostgreSQL.
4. Import an organizer-authored JSON bank with `go run ./cmd/admin import path/to/bank.json`. The import requires exactly 27 Reading and Writing and 22 Math questions and is refused once any attempt exists. The bank format is `{ "questions": [{ "public": Question, "correctAnswer": AnswerValue, "explanation": ContentBlock[] }] }`, using the types in `../SAT_website/src/domain/exam.ts`. Numeric keys may also include `"accepted": ["1/2", "0.5"]`. Keep this private file outside both public repositories. Question image blocks must use embedded `data:image/png;base64,`, JPEG, or SVG URLs; they are only returned with the authorized current section. Keep images small (the import limit is 20 MiB).
5. The frontend already has `.env.development.local` pointing at `http://127.0.0.1:8080`. Restart Vite after changing environment files. Use `http://127.0.0.1` for both apps locally; if you use `localhost` for the frontend, change the API URL to `http://localhost:8080` so the cookie stays same-site.

Without the private question bank, entry is blocked with a clear message. This repository contains no real questions, keys, or sample email credentials. The frontend's development mock remains available by temporarily removing its `VITE_API_BASE_URL` setting.

## Exam behavior

- The server starts Reading and Writing (27 questions, 32 minutes), then switches to Math (22 questions, 35 minutes) immediately on submission or deadline. A background deadline worker advances attempts even if the browser closes. The browser can reload and restore the authoritative attempt and saved answers.
- Answer saves use revisions and mutation IDs. Browser events use event IDs and group related focus/full-screen signals; the count is a record of observations. Events do **not** automatically disqualify. The organizer can lock an attempt with `go run ./cmd/admin lock ATTEMPT_ID "reason"`.
- Each section score is on an **independent Olympiad** 200–800 scale: `200 + 10 × round(60 × correct / questionCount)`. The total is 400–1600. This is not an official SAT score. Numeric fractions and decimals are compared exactly as rational numbers. Tied totals rank by shorter time.
- Results are personal when the test is complete. Correct answers and explanations stay locked until `go run ./cmd/admin release explanations`; ranking stays locked until `go run ./cmd/admin release leaderboard`. The release commands are independent.
- The browser submits no IP field. The server uses its connection's `RemoteAddr` only; no IP eligibility restriction is configured because no policy threshold was supplied. If Railway proxy headers are used for a future IP policy, accept them only from configured trusted proxies.

## Railway

Push this folder to a GitHub repository and create a Railway service from it. Railway detects the root `Dockerfile` and reads `railway.json`; the service listens on Railway's `PORT` and exposes `/healthz`. Add a Railway PostgreSQL service and set backend `DATABASE_URL` to the reference `${{Postgres.DATABASE_URL}}` (adjust `Postgres` if you renamed that service). Set `APP_ENV=production`, `AUTH_SECRET`, exact `ALLOWED_ORIGINS`, `EXAM_OPEN_AT`, `EXAM_ENTRY_CLOSE_AT`, and SMTP variables in Railway. Set a public domain for the API.

For production, build the Vite frontend with `VITE_API_BASE_URL=https://YOUR_API_DOMAIN`. This value is compiled into the browser bundle; it is not a secret. Use a frontend and API under the same site (for example `exam.example.com` and `api.example.com`) with `COOKIE_SAMESITE=lax`. If they must be on different sites, set `COOKIE_SAMESITE=none` and use HTTPS; browsers may still block third-party cookies, so a same-site setup is preferred. `ALLOWED_ORIGINS` must list the exact frontend origin, never `*`.

The backend migration runs on start and uses `CREATE TABLE IF NOT EXISTS`. Make a PostgreSQL backup before schema changes. The organizer CLI needs a database connection to import the private bank and release results; do not expose the CLI as a public endpoint.

## GitHub release

The tag-triggered workflow at `.github/workflows/release.yml` runs tests and publishes server/admin archives for Linux, Windows, and macOS. After connecting a GitHub remote and pushing this repository, tag a version such as `v0.1.0` and push the tag. No GitHub repository or remote was supplied in this workspace, so the release cannot be published until one is provided.

## Verification

Run `go test ./...`, `go vet ./...`, and `go build ./cmd/server`. With a configured PostgreSQL instance, exercise signup, code verification, start, saves, section submission, deadline recovery, release gates, and leaderboard through the frontend. See the detailed route contract in `../SAT_website/docs/go-api-contract.md`.
