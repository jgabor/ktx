---
name: stats
description: Show ktx statistics. Use when the user asks for ktx stats, ktx statistics, a ktx summary, or how many tokens or bytes ktx has saved.
---

# ktx stats

Run this command and show its output to the user unchanged:

```bash
ktx stats summary
```

- Run it from the current working directory; ktx resolves its own database.
- Do not summarize, reformat, or recompute the numbers. Quote the output as
  a code block.
- If `ktx` is not on PATH, say that ktx is not installed or not on PATH and
  stop. Do not install it.
- If the command exits with an error, show the error output as is.
