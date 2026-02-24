# Open Source Release Runbook

## 1) Preflight check

Run the built-in scanner from repo root:

```bash
./scripts/oss-preflight.sh
```

The check fails if:
- blocked internal paths are tracked (for example `.claude/`)
- high-risk secret patterns are found in tracked files
- known production hostnames/relay endpoints are found in tracked files

## 2) Keep local secrets local

Do not publish local generated secret files such as:
- `demo/.demo-env`
- `demo/.demo-secrets.json`

These are ignored by `.gitignore`; verify with:

```bash
git status --ignored --short | rg "demo/.demo-env|demo/.demo-secrets.json"
```

## 3) Publish without old private history (recommended)

If this repository ever contained private planning or infra details in commit history,
create a fresh public repo from the current sanitized tree:

```bash
# from this repo
EXPORT_DIR=/tmp/space-data-network-public
rm -rf "$EXPORT_DIR"
mkdir -p "$EXPORT_DIR"

# copy tracked files from current working tree
# (avoids local untracked files and old git history)
git ls-files -z | tar --null -T - -cf - | tar -xf - -C "$EXPORT_DIR"

cd "$EXPORT_DIR"
git init -b main
git add -A
git commit -m "Initial open-source release"
```

Then push `$EXPORT_DIR` to the new public remote.

## 4) Optional: keep current repository history

Only do this if you explicitly want full history public.
Otherwise use step 3.
