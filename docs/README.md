# docs

`desk.svg`, `info.svg` and `keys.svg` are the screenshots in the top-level
README. They are not mock-ups: each is a capture of a real agentdeck session,
taken with `tmux capture-pane -p -e` and rendered by `ansi2svg.py`, which turns
ANSI colour into one `<text>` run per style — pinned to its column with
`textLength`, so the picture cannot drift when a viewer's monospace font differs.

To regenerate one, capture a desk and render it:

```sh
tmux capture-pane -p -e -t <a client showing the desk> > /tmp/desk.ansi
python3 docs/ansi2svg.py /tmp/desk.ansi docs/desk.svg agentdeck
```

The sessions in the published screenshots are invented — a demo desk built from
stub agents, the same way `internal/e2e` builds one — so no real work appears in
them.
