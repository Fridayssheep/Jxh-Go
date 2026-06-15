# Domain Docs

This repo uses a single-context domain documentation layout.

## Before exploring, read these

- `CONTEXT.md` at the repo root, if it exists
- `docs/adr/`, if it exists, for architectural decisions relevant to the area being changed

If these files do not exist, proceed silently. Producer skills can create them later when domain terms or architectural decisions need to be recorded.

## Layout

```text
/
├── CONTEXT.md
├── docs/adr/
└── docs/agents/
```

## Use domain vocabulary consistently

When naming concepts in issues, specs, code, tests, or refactor proposals, prefer the vocabulary from `CONTEXT.md` once it exists. If a concept is missing, note it as a future documentation gap rather than inventing competing terms.
