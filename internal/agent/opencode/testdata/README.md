# Recorded OpenCode output

The files come from `opencode run --format json` of OpenCode 1.19.5 (`opencode-linux-x64`, SHA-256 `588310a336fa3b9df36b3f0225b26f679d453efaad0cf3d66c3651ef3ec1647a`).
The event shapes (the types, the field names, the order of the lines) come from the 1.19.5 binary.
The values that a model writes (the text, the token counts, the cost) come from a stand-in model endpoint, because the free models of OpenCode were over their quota on the recording host.
The timestamps are the clock of that host. The session ids are random.

- `ok.jsonl`: one bash call, then the answer. Steps end with the reasons `tool-calls` and `stop`.
- `refused.jsonl`: the guard plugin refused the bash call. The call is a `tool_use` line with `state.status` `error` and `state.error` a plain string.
- `noguard.jsonl`: the same forbidden command with no plugin loaded. The call ran, with `state.status` `completed`, and the run exited with code 0.
- `error.jsonl`: an unknown model. The line has a top-level `error` with `name` and `data.message`, and the run exits with code 1.

The constant `pinnedVersion` in `profile.go` names the version of the recording. Record again with a newer OpenCode before you raise it.
