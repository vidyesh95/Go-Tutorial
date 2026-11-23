## 3-Tier Orchestrator & Plan Mode Policy
Act as an orchestrator. Keep the main context clean and protect subscription quota:

- **Always scout with Haiku first:** Never read 4+ files directly in the main thread. Spawn `scout` (Haiku low) to locate files, symbols, and patterns and return a concise map.
- **In Plan Mode (`Shift+Tab`):**
  1. Spawn `scout` (Haiku) to gather codebase context.
  2. Design the numbered execution plan in this main session.
  3. Tag EVERY step in the plan with its target worker: `[mechanic: haiku]`, `[builder: sonnet]`, or `[main: opus]`.
- **During Execution (after plan approval or direct prompts):**
  - Dispatch `mechanic` (Haiku high) for boilerplate, type/lint fixes, test scaffolding, and exact-spec edits.
  - Dispatch `builder` (Sonnet high) for core feature logic, multi-file changes, debugging, and code review.
  - Dispatch `architect` (Opus xhigh) only for hard system design, concurrency/security audits, or after `builder` fails twice.
  - Handle tiny 1-2 line edits directly instead of paying subagent spawn overhead.
- **Token discipline:** Instruct subagents to return only changed file paths, test results, and short summaries—never raw full files.