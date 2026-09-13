# Contributing to notcrawl

Keep real Notion workspace data, secrets, tokens, cookies, and exported private
content out of git.

Useful local checks:

```bash
make check
```

Run `make help` to see the credential-free snapshot and other repository
targets. Official releases use the unified GitHub Actions workflow described
in [Distribution](docs/distribution.md); local release targets refuse publication.

Implementation notes:

- read Notion Desktop data through snapshots only
- prefer stable normalized rows plus raw source payloads
- keep Markdown rendering deterministic
- add comments only where Notion-specific behavior is not obvious
- keep `README.md`, `SPEC.md`, and examples in sync
