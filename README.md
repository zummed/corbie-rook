# Rook

Rook is ready-made [Corbie](https://github.com/zummed/corbie) machines for a bot that answers people on Telegram, Slack, GitLab issues and Jira issues, with agents, approvals and a list of who gets what. `corbie rook init` writes them into a project, Corbie runs them, and the machines are yours to read and change.

## Start

Install [Corbie](https://github.com/zummed/corbie) and [corbie-ui](https://github.com/zummed/corbie-ui), then Rook:

```sh
go install github.com/zummed/corbie-rook/cmd/corbie-rook@latest
```

Make a project, and read its page:

```sh
corbie init --ceiling all
corbie rook init my-bot --bridges slack
corbie rook manual rook-project
```

The manual has three pages of Rook's:

| Page | What it gives |
|---|---|
| `rook-project` | making a project, what to run after a change, and the common changes |
| `rook-design` | the blocks, what each takes and gives, and who may do what |
| `rook-starter` | the files this version starts a project with |

To have a model set up or change a project, run this in the project:

```sh
claude mcp add corbie -- corbie mcp --allow test --allow "rook init"
```

## License

Copyright 2026 Paul Young. Licensed under the Apache License, Version 2.0: see `LICENSE` and `NOTICE`.

Rook was written with Claude Code, designed and directed by Paul Young.
