<p align="left">
   <strong>English</strong> | <a href="./README.CN.md">简体中文</a>
</p>

# ai-turn-changes

Track what your terminal AI agent actually changed, turn by turn.

ai-turn-changes is a VS Code extension for inspecting code changes made by terminal-based AI coding agents such as Claude CLI or Codex CLI. Instead of waiting until everything is flattened into a commit, it captures changes as turn-sized units, keeps their history, and renders them with much richer context than a raw diff dump.

If you use terminal agents interactively, this project gives you a missing layer between "the model just changed something" and "I am ready to commit this." Git stays in the loop, but turns become the first-class unit for review.

![image](./public/dark.png)

## Why this exists

- `/diff` is good for the current exchange, but it does not give you durable turn history.
- Git diff is commit-oriented, while AI agent workflows are often turn-oriented.
- Reviewing only the changed lines is not enough when you need full-code context to understand what actually happened.
- Git is still used under the hood, but only as the workspace boundary and change-detection layer.

## What it does

When you run an interactive CLI agent, the extension captures the execution as a series of turns and lets you inspect the resulting code changes inside VS Code.

- Manual mode: explicitly start and finish a turn, then inspect the resulting changes.
- Automatic mode: bind an active terminal and let the extension infer turn boundaries from terminal activity and workspace changes.
- Turn-oriented history: browse earlier turns instead of only the latest one.
- Rich diff reading: open a dedicated turn diff viewer instead of relying on a plain terminal diff dump.

## How it works

- Manual mode: you decide where a turn starts and ends. The extension snapshots the baseline, captures the resulting workspace state, and computes the diff.
- Automatic mode: bind an active terminal, then let the extension observe terminal output, terminal state, and workspace file changes to infer when one AI activity segment has settled.
- File tracking: Git is used to determine the observable file set and respect `.gitignore` rules.
- Snapshot storage: relevant file contents are stored as Snapshots in the extension's local storage. Snapshot lifecycle is managed with reference counting.
- Boundary detection: automatic mode classifies terminal states such as thinking, waiting for approval, in-progress summaries, prompt restoration, and fallback quiet windows.

## Usage

- Manual mode: start a turn, let the agent work, finish the turn, inspect the result.
- Automatic mode: bind an active terminal with `Bind Active Terminal`, switch to automatic mode, and let the extension track turns on its own.

### Sidebar layout

- Session: shows the current mode, turn state, and terminal binding status.
- Actions:
  - Start Turn
  - End Turn
  - Switch Mode
  - Bind Active Terminal
- Turns: shows recorded turns and the files changed in each turn.

## Philosophy

- Git-compatible, but turn-native.
- Commits are for repository history.
- Turns are for understanding what the agent just did.
- The extension does not replace Git. It adds a more useful review layer before commit time.

## Development

1. `npm install`
2. `npm run compile`
3. Press `F5` in VS Code to launch the Extension Development Host

## License

MIT License
