# Benchmark artifact cleanup implementation plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Keep benchmark results reviewable in PR #10 without committing raw run artifacts or deleting local data.

**Architecture:** Make one additive cleanup commit on the existing PR branch. Untrack only PR-added raw files under `bench/results/`; preserve inherited upstream files, benchmark source, and Markdown reports. Ignore future raw results and retain the current results and limitations in repository documentation.

**Tech Stack:** Git index operations, ignore rules, Markdown, Python verification, Go 1.27.1 validation gates.

---

### Task 1: Preserve local evidence and shrink the PR

**Files:**
- Modify: `.gitignore`.
- Retain/update: Markdown results reports in `bench/results/` and `plans/validation.md`.
- Create if needed: `bench/RESULTS.md`, linking the latest results and local evidence locations.
- Untrack, without deleting: raw non-Markdown outputs added under `bench/results/` since the PR merge base, excluding source helpers such as `profile-20261006/route_runner.py`.
- Local verification records: ignored `tmp/benchmark-cleanup/`.

1. Verify the clean integration worktree at `69332d3` and PR head `sebishogun:engine`; compute its merge base with upstream `main`.
2. Inventory PR-added raw artifacts and record each local path, size, and SHA-256 before changing the index. Exclude benchmark source helpers regardless of directory. Do not remove upstream-owned artifacts or any local file.
3. Add ignore rules that keep new raw result files local while allowing Markdown reports. Untrack only inventoried artifacts using `git rm --cached`, never filesystem deletion.
4. Keep the final Rust, native C, and SIMD results with their separate measurement conditions and caveats in repository reports. Label raw evidence paths as local rather than committed links. Do not change measured numbers or application code.
5. Verify every inventoried local file still has its original size and hash, is absent from the index, and is ignored. Verify inherited upstream result files and harness source remain tracked; inspect the complete staged diff.
6. Run formatting checks, bounded Go 1.27.1 vet and root/nested race tests with SQLite FTS5. No benchmarks, watchers, or automatic test retries.
7. Review spec compliance, then diff quality. Create exactly one cleanup commit and fast-forward push `HEAD:engine` to `fork`, without rewriting history.
8. Verify the remote PR head, smaller net diff, retained report, and local artifact manifest after pushing.
