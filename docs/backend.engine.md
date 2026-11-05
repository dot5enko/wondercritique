# Backend — pipeline engine and runs API

**State as of 2026-11-04** (canvas integration: `context_paths`, canvas context, commit of results;
agent-mode runs). Phase 6 of `plan.critique-engine.md`: a run executes a pipeline (generate →
render → critique → gate loop → output) whose steps, artifacts and live events the UI shows.
**The design chat runs in agent mode** (`agent: "canvas-chat"`, a synthesized Input → Agent
graph, reply + gen_components results) — see `docs/backend.chat-agent.md`. Package `internal/features/engine` (`engine.Module`), models in
`internal/models/run.go`, node-agent bridge in `internal/features/agentrun/node.go`.

Verify: from `apps/backend`, `go test ./internal/features/engine ./internal/features/agentrun`
(use `-race`; the engine tests run against a temp-file WAL SQLite like production — a shared-cache
in-memory DB answers parallel writers with SQLITE_LOCKED). Tests use a fake `NodeRunner` and a fake
`render.Renderer`, so no model, Chrome or network is involved.

## Data model (`runs`, `run_steps`, `artifacts`)

- `Run`: owner, `thread_id` (= id of the thread's first run), `mode` (pipeline|agent), `agent_id`,
  `reply` (chat agent answer), `context_paths` (JSON, selected
  canvas files), `canvas_folder` (default destination at start → folder used after commit),
  `canvas_notes` (JSON, the user's canvas operations delivered to this run), pipeline def id/slug/name, pinned
  `pipeline_version` + `pipeline_version_id` (`studio_def_versions.id`; both empty for draft test runs),
  `snapshot` (the pipeline body executed — runs never see later edits), `is_test`, `input` (JSON of
  strings; images are `wcblob://` refs), `status` (pending|running|completed|failed|cancelled|
  interrupted), `iteration`, `best_artifact_id`, `best_score`, `usage` (JSON serializer), `cost`
  (nil when the model has no models.dev price), `error`, `started_at`/`finished_at`.
- `RunStep`: run, iteration, node id/type, status, `pinned_versions` (`{agent, model_set}`), `model`
  (`provider/model`), `agent_session_id` (agentkit session in the shared sqlite store = the
  transcript), `output` (JSON), `usage`, `error`, times, `artifact_ids`.
- `Artifact`: run, iteration, node, `kind` (html|image|json), `blob_sha`, `score`. Content lives in
  the blob store: html `text/html; charset=utf-8`, shots `image/png`, critiques/reports
  `application/json`.
- GORM gotcha: columns with `serializer:json` (usage, pinned versions, artifact ids) are only
  serialized by struct updates — the engine uses `Select(...).Updates(struct)`, never map updates.

## HTTP (`/api/v1`, any authenticated user; runs are owner-scoped, admins see all)

| Route | Notes |
|---|---|
| `POST /runs` | `{input, pipeline_id?, pipeline_version_id?, agent?, thread_id?, is_test?, context_paths?}` → **201** `{id, thread_id, status}`; the run executes in the background; `agent` = agent mode |
| `GET /runs` | `?limit` (≤200, default 50) `?offset` `?filter[thread_id]` → `{items, next_cursor: null}`, newest first; item = id, status, pipeline ref, iterations, best_score, usage, cost, created_at, thread_id, input, context_paths (always an array), canvas_folder, mode, reply |
| `GET /threads` | (2026-11-05, `threads.go`) chat list: `?limit` (≤100, default 30) `?offset` `?q` → `{items, next_offset}` (`null` on the last page); item = `id` (thread id), `title` (first run's trimmed `brief`, "" if none), `status` (latest run), `created_at` (first run), `updated_at` (latest run — order, newest first), `runs`. `q` = case-insensitive substring of the title, matched in Go (`strings.ToLower`, Unicode-safe; SQLite `lower()` is ASCII-only) over the viewer's thread heads (`id = thread_id`), then `thread_id IN …`; follow-up briefs are not searched. Paging = `GROUP BY thread_id ORDER BY MAX(created_at) DESC` with `limit+1`. Owner-scoped like `/runs`. Test: `threads_test.go` |
| `GET /runs/{id}` | run + `steps[]` + `artifacts[]` + `results[]` (`{artifact_id, position, score}`) + `pipeline_body` (the pinned snapshot, so non-admin viewers get node labels/types without the studio API) + `context_paths`, `canvas_folder`, `canvas_notes`, `canvas_paths` (live files the run committed; arrays never null), `mode`, `agent_id`, `reply` |
| `GET /runs/{id}/events` | SSE; replays the run's history, then live; closes after `run_completed`/`run_failed`; `: ping` every 15 s. SSE requests skip the 60 s request timeout |
| `POST /runs/{id}/cancel` | 204; 409 when already finished; a stale (not executing) pending/running row is marked cancelled |
| `GET /artifacts/{id}/content` | blob with its stored MIME; `nosniff` + `Content-Security-Policy: … sandbox`; access follows the run's owner scoping |

Start rules (`start.go`): pipeline = explicit version id → explicit `pipeline_id` (latest published)
→ the published pipeline with slug `default` (else the first published). `is_test` is admin-only and
lets a run execute the pipeline's/agents' **drafts**. The pipeline is validated with
`studio.ValidatePipelineBody` (error-severity issues → 400) and by `buildGraph` (unknown nodes,
cycles). Input values are strings or `{artifact_id}` (image artifact → `wcblob://`, html/json → text);
`data:image/…;base64` strings (≤ 8 MB) are stored as blobs; field names `^[a-zA-Z_][a-zA-Z0-9_-]{0,63}$`;
required fields declared on the Input node (`config.fields`, `required` default true) must be
present, and a run needs at least one non-empty value. `thread_id` must be a thread the caller owns
(404 otherwise). Responses use the shapes of `apps/frontend/src/api/runs.ts`.

## Execution model (`exec.go`, `nodes.go`, `graph.go`)

- **Graph**: non-loop edges form a DAG; nodes run level by level (longest-path depth), nodes of a
  level in parallel; the first failure cancels its siblings and fails the run with
  `"{node label}: {error}"`. At most one loop edge (Gate `continue` → e.g. generator `feedback`).
- **Iterations**: run the whole graph; after it, if the Gate chose `continue` (and a loop edge
  exists) the next iteration starts with the Gate's feedback delivered to the loop target port
  (empty on iteration 1). Hard cap 10 iterations.
- **Skipping**: a node is skipped (no step row) when a connected input port received no value, i.e.
  its source was skipped or was an unfired Gate output. This is how `gate.done → output` only runs in
  the final iteration.
- **Events** (`bus.go`, payload repeats `event` and `run_id`): `iteration_started`, `step_started`,
  `step_completed` (status, usage, artifact_ids, error), `iteration_completed` (best_score,
  artifact_ids), `run_completed` (best_artifact_id, usage, cost, result_artifact_ids,
  canvas_paths — also in the synthesized terminal event), `run_failed` (error, status; also
  sent for cancelled/interrupted runs). History is bounded (4000) and kept 5 min after the run; a
  slow subscriber is dropped (client reconnects and replays). A run unknown to the bus gets its
  terminal state synthesized from the DB.
- **Concurrency**: `ENGINE_CONCURRENCY` (default 4) runs execute at once; the rest wait `pending`.
  Runs execute under the engine's root context (not the HTTP request); `Close` (fx stop) cancels
  them and they end `interrupted`. Startup marks leftover pending/running runs and steps
  `interrupted` ("server restarted"). Resume is not implemented.

### Nodes

| Node | Behaviour |
|---|---|
| `input` | ports `brief` (`briefOf`: `brief` else first text value) and `references` (all `wcblob://` inputs); step output = the input map |
| `agent` | `agentrun.RunNode` (below). Prompt variables = all run input fields + `canvas` + `history` (agent mode), overridden by wired ports (same name; a list of images stays a list). Contract artifact → ports `html`/`artifact` + html artifact (becomes the iteration's *candidate*); critique → port `critique` + json artifact with score; custom → port `output` + json artifact; chat → reply (`run.reply`, port `output`) + the gen_components components as the iteration's set (`candidates`). Config: `agent_id`, `agent_version` (pin), `model_set_id` (override); 30 min timeout |
| `validate` | `component.Validate` → port `report`, json artifact (never fails on violations) |
| `render` | `render.Renderer` (config `width` 1280, `dpr` 2, `color_scheme`, `variants[]`); ports `images` (`wcblob://` per shot, one image artifact each) and `metrics`; step output keeps metrics minus `text_runs`; 90 s timeout. `ErrNoBrowser` → "no Chrome/Chromium was found on the server (set RENDER_CHROME_PATH)" |
| `merge` | critiques (`dedupe` default true) → mean score, strengths/issues merged (issues deduped by code+message, highest severity, ordered error→info); json artifact with score |
| `gate` | judges its `critique` input (score 0–10); see below. `continue` port carries the feedback text, `done` port the iteration's candidate HTML. Step output `{decision, reason, score, iteration}` |
| `output` | picks the final iteration per `strategy`: `best` (highest score, ties → latest) or `last`; sets the run's `best_artifact_id`/`best_score`. If no candidate artifact exists but an `html` text is wired in, that text becomes the html artifact |

### Scores, gate, feedback

- Critique scores are **0–10** (clamped when parsed). An iteration's score = the gate's input score,
  else the merge score, else the mean of the critics. It is attached to the iteration's candidate
  HTML artifact when the iteration closes (UI reads `artifact.score`).
- Gate config: `min_score` (default **8** when unset/0), `max_iterations` (default 3, hard cap 10),
  `patience` (default 1: stop after that many consecutive iterations without beating the best
  earlier score), `budget_usd` (stops once the known cost reaches it), `expr` is **not**
  implemented (ignored). Order of checks: score ≥ target → no loop edge → max iterations →
  patience → budget → otherwise continue.
- Feedback for the loop target (`feedbackText`): `## Feedback on your previous attempt (iteration N)`
  + the formatted critique + `## Previous HTML` (fenced, ≤ 60 000 chars) + a revise instruction. So a
  regenerating agent sees both what was wrong and the code it wrote.
- Canvas context, selection and commit: see "Canvas" below.
- Cost: `Σ ModelCost.Estimate(usage)` over steps whose model has a models.dev price (the catalog
  parser now keeps per-model `cost`; `llm.Service.ModelCost(provider, model)`); unpriced → `cost`
  nil and budgets are not enforced.

## Canvas (2026-11-04, `context.go`, store in `docs/backend.canvas.md`)

- **Start** (`start.go`): `context_paths` (≤ 12 entries, each a live file or folder of the thread;
  folders expand recursively, deduplicated; > 12 files after expansion, unknown/invalid paths or a
  missing `thread_id` → 400 field error `context_paths`; foreign thread → 404 via `checkThread`) →
  `run.context_paths` (expanded file paths). `run.canvas_folder` = default destination: always a
  new top-level `/<BriefSlug(brief)>` regardless of the selection, made unique (`freshFolder`)
  against the thread's live folders **and** the `canvas_folder` of its pending/running runs, so a
  pending run already shows as its own group and parallel runs don't share one. The run row is created in
  one `dbx.Tx` with `canvas.TakeNotes` (texts on `run.canvas_notes`, each note reaches one run).
- **Canvas context** (`buildCanvasSeed`) — iteration 1's loop seed (reaches agents whose template
  uses the loop port, e.g. `{{feedback}}`) and the prompt variable `canvas` (`nodeRun.samples`,
  agent nodes): `## Canvas` (files are readable under /canvas, not changeable) → `### Files`
  (indented tree, per folder files first then subfolders, `name — score 8.6, message N`, ≤ 200
  lines) → `### Changes the user made since the last message` (notes + "don't build on, reference
  or recreate them") → `### Selected components` (path + fenced HTML) **or**, without a
  selection, `### Latest result` (≤ 3 live files of the run that committed the newest file) →
  `### Where your result goes` (never modify files; the result becomes new files in the new
  top-level folder `run.canvas_folder`; submit_artifact `folder` = a name for that group, `names`). HTML budget ≈ 60 000 chars, ≥ 2 000
  per file. nil (no seed, no variable) for a thread with no files, notes or selection — a fresh
  chat's first run gets no seed.
- **Destination hints**: an artifact-contract agent's `NodeResult.Folder/Names` (submit_artifact
  `folder`/`names`) are recorded per iteration (`iterState.dest` → `iterRecord.dest`) and as the
  run's latest hint; the final iteration's hint wins, else the latest given. `count>1` / pick_best
  workers don't produce hints (only the orchestrating agent's final submit does).
- **Commit** (`finish` → `commitCanvas`, same tx as `result_artifacts`; on error the results are
  stored without canvas files and the error logged): item i = result i; folder = the hint
  flattened by `canvas.TopFolder` (`Pricing/Dark` → `/pricing-dark`) else `run.canvas_folder`;
  always `Fresh` (made unique against live folders — results never join an existing folder);
  name = `names[i]` → the selected file's name (one file selected, one result) →
  `<BriefSlug>.html` (one result) / `<BriefSlug>-<i>.html`; `source_path` = the selected file when
  exactly one was selected; score = result score. `run.canvas_folder` is updated to the folder
  used. Failed/cancelled runs commit nothing.
- `RunDetail.canvas_paths` / `run_completed.canvas_paths` = `canvas.PathsOfRun`.
- Tests (`engine_test.go`): `TestCompletedRunCommitsToTheCanvas` (default folder, follow-up seed =
  tree + latest result, `canvas` variable, `ThreadID` on node requests),
  `TestSelectedComponentsSeedAndPlaceTheResult` (selection seed, `/dark/login-form.html`
  with `source_path`, validation incl. folder expansion past 12), `TestAgentDestinationHintsWin`,
  `TestEveryResultGetsItsOwnTopLevelFolder` (parallel runs reserve `/pricing-2`, `/pricing-3`; a
  hint naming an existing folder → `/pricing-4`),
  `TestCanvasNotesReachTheNextRunOnce`; failed runs commit nothing; SSE carries `canvas_paths`.

## Agent bridge (`agentrun/node.go`)

`Runtime.RunNode(ctx, NodeRequest{AgentID, Version, Draft, ModelSetID, Samples, Owner, ThreadID,
Title, OnEvent}) (*NodeResult, error)`:

- `ThreadID` (set by the engine for agent and pick_best judge nodes) goes into the session
  metadata (`{"owner", "thread_id"}`) and the `nodeScope`, so nested runs of the candidate tools
  pass it on; it makes the session mount the thread's canvas at `/canvas`.

- Body resolution: `Version>0` → that published version; `Draft` → the draft when present (else
  latest published); otherwise latest published (`NotFound` when unpublished).
- Builds are cached per `node|defID|version|modelSetID` (name `slug@v{n}`, `+{msid}` with an
  override) on the shared sqlite store, so every node run leaves a persisted session; draft builds
  (`slug@draft-xxxxxxxx`) are not cached and drop their read-only material afterwards.
- Output parsing per contract: artifact → `ExtractHTML` (strips a markdown fence, must contain `<`),
  critique → `ParseCritique` (JSON object, score required, severity normalized, score clamped),
  custom → the raw JSON object. An unusable answer gets **one** corrective user message in the same
  session ("Your answer could not be used (…). Call submit_artifact now…"); a second failure fails
  the node (`agent "slug": …`).
- `NodeResult` carries `Folder`/`Names` (canvas destination hints from the submit_artifact
  envelope), `Usage`, `SessionID`, `Steps`, `AgentSlug/Version`, `ProviderID/ModelID`,
  `ModelSetVersion`. `BuildInput` now accepts lists of image refs (`[]string`/`[]any`) as one
  prompt variable (each becomes an image block; preview shows `[image: name]`).

## Multi-candidate results (updated 2026-11-02)

- Table `result_artifacts(run_id, artifact_id, position, score)` (`models.ResultArtifact`), written
  in `finish()` for completed runs: one row = the selected final artifact (with its score); several
  rows = a multi-component result ("give me 5 buttons"). `run_completed` event carries
  `result_artifact_ids`.
- Agent node config `count` (>1) runs the agent in parallel (`agentrun.GenerateCandidates`, optional
  `variations[]` hints) → output port `candidates` (port type `candidates`, items = `portVal.Items`).
  An agent that submits several components (`submit_artifact paths`) also emits `candidates` and no
  `html`, so render/critics/gate/output are skipped (engine skips nodes with an unfired connected
  input) and the iteration's `set` (`iterState.items`) becomes the result.
- `pick_best` node (inputs `candidates`, `brief`; outputs `html`, `ranking`): renders each
  candidate (`renderVariants`, dpr 1), runs the judge agent (`judge_agent_id`, default slug
  `vision-judge`) via `agentrun.Judge`, sets the winner as the iteration candidate. A single
  candidate passes through.
- `stepSink` (agentrun.Sink via ctx) records tool-made artifacts on the step and sums nested-run
  usage/cost; step output includes `tools` events (`generated`, `picked`); `Component` records a
  gen_components result (html artifact + name/folder/source hints, see
  `docs/backend.chat-agent.md`). `agentNodeTimeout` = 30m.

## Known limits / future work

- No mid-run resume; `gate.expr` unsupported; Output node
  ports beyond `html`/`critique` are decorative (selection is engine-side); `GET /runs` has offset
  paging only; no retention/cleanup of runs, blobs or agent sessions.
- Chrome must exist on the host for the render node (see `docs/backend.render.md`); without it a
  run fails at the Render step with a readable message.

## Live activity (updated 2026-11-02)

- SSE event `step_activity {iteration, node_id, label}` (deduplicated per step in `nodeRun.activity`).
  Sources: agent run events via `agentrun.ToolActivity` (Thinking…, Writing a file, …), the
  candidate tools/`GenerateCandidates`/`Judge` through `Sink.Activity` ("Generated 3/5 candidates",
  "Rendering candidate 2/5", "Judge comparing 5 candidates"), and the render/pick_best nodes.
