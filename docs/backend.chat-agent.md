# Backend — design chat = one agent (agent-mode runs, gen_components)

**State as of 2026-11-05** (vision mode added). The design chat (canvas) no longer runs a pipeline per message: it
talks to one studio agent, `canvas-chat` (system prompt + tools). Generation is a tool call,
`gen_components(count, description, …)`; every generated component becomes a result — nothing is
judged or reduced to one. Pipelines stay (studio, explicit `pipeline_id` runs) for more complex
flows later. Engine details: `docs/backend.engine.md`; agent runtime: `docs/backend.studio.md`.

Verify: from `apps/backend`, `go test -race ./internal/features/engine -run TestChatAgent` and
`./internal/features/agentrun -run TestGenComponents`.

## Agent-mode runs (`engine/start.go`)

- `POST /runs` `{agent: "<slug or id>", input, thread_id?, context_paths?}` → agent mode;
  `agent` + `pipeline_id`/`pipeline_version_id` → 400; unknown agent → 404; unpublished → 400.
  Without `agent` the old pipeline resolution applies (unchanged default = pipeline `default`).
- The run executes a **synthesized graph** (`chatGraph`): `input` → `chat` (agent node, label =
  agent name, `agent_id`, `agent_version` = latest published; admin `is_test` runs omit the pin →
  the agent's draft). Snapshot stored as usual, so steps / SSE / run detail / graph work as for
  pipelines (2 steps: input, chat). No pipeline validation for it.
- `runs` columns: `mode` (`pipeline` | `agent`; old rows "" → reported `pipeline`), `agent_id`,
  `reply` (text). Agent mode: `pipeline_id`/`slug` empty, `pipeline_name` = agent name.
- Prompt variables of the chat agent: `brief` (the message), `canvas` (canvas seed; the
  "Where new components go" section tells it to use gen_components `folder`/`names`/`sources`
  instead of submit_artifact), `history` (`buildHistory`: `## Conversation so far`, the last 12
  earlier messages of the thread — `User:` brief, `(about …)` context paths, `You:` reply ≤ 2000
  chars, `Files created:` live canvas paths of that run, status note for failed/running; "" for
  the first message).

## Vision mode (2026-11-05, `engine/vision.go`)

The canvas chat agent has **no visual tool** of its own (its tools are read_file, glob, grep,
gen_components; the render service is only used by pipeline Render / pick_best nodes and the
canvas PNG export). Instead a message can be sent in vision mode:

- `POST /runs` `vision: true` — agent mode only, needs `context_paths` resolving to 1…
  `maxVisionShots` (6) files (400 field `vision` otherwise). Stored as `runs.vision`; `GET /runs`
  items and run detail carry `vision`.
- When the agent step runs (`execAgent` → `visionSamples`), each selected file is rendered with
  `canvas.Screenshot` (the export look: 1280 stage, light, narrow content cropped) at **1x**
  (`visionDPR`), activity "Rendering preview i/n", stored as an image artifact of the step. The
  agent gets the refs as the prompt variable `previews` (not in the template → `BuildInput`
  appends them as image blocks after the text) and the brief prefixed with
  `<meta>Vision mode: the attached image(s) … in this order: <paths>. Look at … and try to fulfill
  the user's request below, judging by what is actually rendered …</meta>` (+ files that failed to
  render). The stored run input stays the user's text. All renders failing fails the step
  ("could not render a preview: …").
- History marks such messages "(you were shown screenshots of them)".
- Test: `TestChatAgentVisionMode` (`chat_test.go`).

## `chat` output contract

- Agent body `output_contract.type = "chat"`: no terminal submit tool; the final text is the
  reply (`NodeResult.Text`, no parsing/nudge). `## Output` section says so and forbids pasting HTML.
- Engine agent node, contract chat (`nodes.go`): the components the step's sink collected
  (`stepSink.Component`, in call order) become `it.items` (a set result → `result_artifacts` →
  canvas commit); reply → `runState.reply` → `run.Reply`; step output `{reply, items, tools}`.
  A reply-only message completes with no results and commits nothing.
- `run_completed` SSE (+ synthesized terminal event) carries `reply`; `GET /runs` items and
  `GET /runs/{id}` carry `mode`, `reply` (detail also `agent_id`).

## `gen_components` tool (`agentrun/tools_gen.go`)

Input: `count` (default 1, max `tool_config.gen_components.max_count`, default/hard cap 8),
`description` (required: the full brief for the generator — it doesn't see the chat),
`variations[]` (direction per component), `sources[]` (canvas paths, `/canvas` prefix optional,
read through the session's `/canvas` mount and appended to the worker input as `## Source
components` with fenced HTML ≤ 30 000 chars each), `folder`, `names[]`.

- Worker = `tool_config.gen_components.agent_id`, else the agent with slug `component-generator`
  (`studio.DefaultWorkerSlug`); runs via `GenerateCandidates` (parallel, ≤ 4 at once,
  `Worker: true` → candidate tools stripped). Worker samples = caller samples minus
  canvas/history/feedback/critique/html, `brief` = description. One component without a
  variation hint gets no "candidate 1 of 1" block.
- Each success: copy in the caller's `/work/components/<name|component-N>.html`, validator
  report, and `Sink.Component{HTML, Name, Folder, Variation, Source}` (Source = the single
  source path when exactly one was given). Returns JSON `{components: [{path, name, variation,
  bytes, valid, violations}], failed, note}`. Without a sink (Try-it) only the workspace copies.
- `gen_components` is in `candidateTools` (stripped from worker builds, 20-min tool timeout,
  refuses inside nested runs).
- Commit (`engine/context.go` `commitCanvas`): folder = first non-empty `folder` hint (flattened,
  always a fresh top-level folder) else `run.canvas_folder`; name = `names[i]` → `baseOf(source)`
  for a single result → `<BriefSlug>.html` / `<BriefSlug>-<i>.html`; `source_path` per item from
  the hint's `sources[i]` (else the single selected file).

## Seeded `canvas-chat` agent (`studio/defaults_chat.go`)

`ensureChatAgent` (fresh seed and every start of an already-seeded install; re-created if
deleted; skipped without model set `default`): contract chat, tools `read_file, glob, grep,
gen_components`, input template `{{history}}\n\n{{canvas}}\n\n## Message\n\n{{brief}}`, system
prompt: talk + create via gen_components; "N variants" → `count` N, keep all of them, never pick a
best unless asked; separate calls for different kinds; `sources` to build on existing files;
answer questions without generating; short reply after generating, no HTML in the reply.

## Style guide

A chat may follow a style guide picked with its first message (`styleguide_id`, fixed per
thread): canvas-chat and its gen_components workers get it in the system prompt and at
`/styleguide/STYLEGUIDE.md` — `docs/backend.styleguide.md`.

## Frontend

The canvas sends every message with `agent: 'canvas-chat'` and `input: {brief}` (the old
default-pipeline field lookup is gone); the reply shows in the group header / message card (see
`docs/frontend.studio-ui.md` "Design canvas").
