# models.dev catalog (external LLM provider/model registry)

Researched 2026-11-01 from `https://models.dev/api.json` (~6 MB, no auth). Snapshot used for research:
`tmp/modelsdev/api.json`. Not wired into the backend yet.

## Shape

Top level: object keyed by provider id (235 providers, 8 940 models on 2026-11-01).

Provider: `{id, name, env[], npm, api?, doc, models{}}`
- `env`: env var names for the API key (e.g. `ANTHROPIC_API_KEY`; Google lists three alternatives).
- `npm`: the Vercel AI SDK package = **wire protocol hint**. Distribution: `@ai-sdk/openai-compatible` 196,
  `@ai-sdk/anthropic` 8, `@ai-sdk/openai` 6, `@ai-sdk/azure` 2, one each for google, google-vertex, groq, xai,
  mistral, cerebras, cohere, deepinfra, togetherai, perplexity, amazon-bedrock, openrouter, gateways, etc.
- `api`: base URL; present on 210 providers, always present for `openai-compatible`; absent for first-party
  SDK providers (`openai`, `anthropic`, `google` → SDK default URL). May contain `${ENV_VAR}` placeholders.

Model: `{id, name, description, family, attachment, reasoning, reasoning_options[], tool_call,
structured_output, temperature, modalities{input[], output[]}, limit{context, output}, cost{input, output,
cache_read…} (USD / 1M tokens), release_date, last_updated, knowledge?, open_weights, status?, type?,
canonical_model_id?, provider?, interleaved?, experimental?}`
- `type` absent (8 489) = chat LLM; otherwise `embedding`, `image`, `video`, `speech`, `transcription`,
  `reranking`, `realtime`, `chat`.
- `status`: absent, `alpha`, `beta`, `deprecated` (899).
- Vision = `modalities.input` contains `"image"`; 5 283 models have image input **and** `tool_call`.
- `reasoning_options[].type` ∈ `effort` (with `values`), `budget_tokens`, `toggle`.
- Per-model `provider` override: `{npm?, api?, shape?}`; `shape` ∈ `responses` | `completions` (OpenAI
  Responses API vs Chat Completions).

## Meta entry

`meta`: `npm @ai-sdk/openai` (OpenAI Responses protocol), `api https://api.meta.ai/v1`,
`env ["META_MODEL_API_KEY"]`. Models: `muse-spark-1.1`, `1.2`, `1.2-contributor`, `1.3`, `1.3-contributor`,
`muse-wonder-1.0`. `muse-spark-1.3-contributor`: image/video/pdf/audio input, tools, reasoning effort
minimal…xhigh, structured output, context 1 048 576, output 131 072, cost in 0.1 / out 0.2. The backend's
existing `agentkit/llm/meta` provider speaks this Responses protocol (but reads `MODEL_API_KEY`, not the
models.dev env name).
