#!/bin/bash
# lat.md prompt hook for Claude Code
# Injected by `lat init` — reminds the agent to consult lat.md before working.
#
# This hook fires on every user prompt submission. It adds a short instruction
# telling the agent to run `lat search` and `lat prompt` so that lat.md context
# is consulted before any code changes.

cat <<'HOOK_JSON'
{
  "hookSpecificOutput": {
    "hookEventName": "UserPromptSubmit",
    "additionalContext": "Before starting work on this task:\n1. Run `lat search` with a query relevant to the task and read the results to understand the design intent.\n2. If the prompt contains [[refs]], run `lat prompt` on the full prompt text to resolve them.\n3. After completing work, run `lat check` to validate all links and code refs.\nDo not skip these steps."
  }
}
HOOK_JSON
