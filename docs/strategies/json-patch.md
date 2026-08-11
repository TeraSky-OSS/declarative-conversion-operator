# JSON Patch

## What it does

The escape hatch: applies a raw [RFC 6902 JSON Patch](https://www.rfc-editor.org/rfc/rfc6902) document per direction, for conversions none of the other named strategies can express.

## When to use it

Only when nothing else fits. Every other strategy documents specific, analyzable semantics the engine can reason about; `jsonPatch` is an arbitrary escape hatch the engine treats as a black box. Prefer a named strategy whenever one applies — it's self-documenting and the engine can actually verify its coverage and losslessness. Reach for `jsonPatch` for genuinely one-off structural moves that don't fit any named pattern (the built-in `move` operation is the most common use, as shown below).

!!! lossy "Always lossy unless `losslessOverride: true`"
    The engine cannot statically verify what an arbitrary JSON Patch document actually does, so both directions default to lossy (`acknowledgeLossy: true` required) unless you explicitly set `losslessOverride: true` — at which point you're personally vouching for correctness, verified by running representative samples through [`convctl test`](../cli.md), not by anything the compiler checked.

## Example

The hub's `legacyFlag` needs to become `legacyFlagV2` on the v2 spoke — a plain rename would work too ([Field Rename](field-rename.md) is usually the better choice for this exact case), but here it's shown via `jsonPatch`'s `move` operation to illustrate the escape hatch:

```yaml
- strategy: JSONPatch
  jsonPatch:
    hubToSpoke:
      - op: move
        from: /spec/legacyFlag
        path: /spec/legacyFlagV2
    spokeToHub:
      - op: move
        from: /spec/legacyFlagV2
        path: /spec/legacyFlag
    losslessOverride: true
```

### Objects

=== "Hub (v3)"

    ```yaml
    spec:
      legacyFlag: true
    ```

=== "Spoke (v2)"

    ```yaml
    spec:
      legacyFlagV2: true
    ```

## Coverage tracking

Even though the engine can't verify *what* a patch does, it still tracks *which paths* it touches for the fail-closed uncovered-field check: every path mentioned by either direction's patch (`path` or `from`) is claimed on **both** the hub and spoke side, even if a given path is only ever referenced by one direction's patch. This deliberately over-claims rather than leaving a JSON-Patch-touched field permanently flagged as uncovered — appropriate for a strategy whose entire nature is "trust the author" — while a genuine conflict with another rule targeting the same path is still caught.

## Supported operations

All six RFC 6902 operations are supported: `add`, `remove`, `replace`, `move`, `copy`, `test`. `from` is required for `move`/`copy`; `value` is required for `add`/`replace`/`test`.
