# Project Constraints

## Desktop Packaging

- After modifying source code, finish the task by packaging the desktop application. A frontend-only build does not satisfy this requirement.
- On Windows, use `bash scripts/build-desktop.sh` from the repository root unless the task requires installer packaging. Use `bash scripts/build-desktop.sh --nsis` when an NSIS installer is explicitly required.
- Packaging is successful only when the artifact produced by the current task exists and is non-empty. The canonical artifact is `cmd/ainovel-desktop/build/bin/ainovel-desktop.exe`.
- If Windows has the canonical executable locked by a running instance, do not terminate the user's process. Build without `-clean` and use `wails build -o ainovel-desktop-<timestamp>.exe ...` to create a new artifact in `cmd/ainovel-desktop/build/bin/`, then report both the lock and the alternate artifact name.
- In the final response, report the desktop artifact path and its size together with test/build results.
- If packaging cannot complete because of a missing external dependency or environment failure, report the exact blocker and the command output; do not claim the task is complete.
