# CEL

## What it does

An explicit last-resort escape hatch: two [CEL](https://cel.dev) expressions rewrite declared hub and spoke paths. The `object` variable is the source object (`dyn`). Each expression must return a **map**; only the rule's declared destination paths are copied from that map onto the output (nested paths or dotted keys both work). Extra keys in the result are ignored.

## When to use it

A transform that none of the declarative strategies can express — for example packing one integer into two 8-bit halves. Prefer [JSON Patch](json-patch.md) for path-only surgery and the named strategies for everything they cover. CEL is for genuinely arbitrary value math.

!!! lossy "Always lossy — no `losslessOverride`"
    Compile cannot prove the two expressions are inverses. `acknowledgeLossy: true` is **required** at admission and at compile time. Unlike `jsonPatch` / `scalarToFields`, there is no `losslessOverride` escape. Prove a particular expression pair on your data with [`convctl test`](../cli.md).

Coverage still fail-closes: `hubPaths` / `spokePaths` are claimed so those fields are not reported as uncovered. The engine does not inspect the expression bodies beyond parsing them.

## Example

The hub stores `spec.packed`; v1 splits it into `spec.bitHigh` and `spec.bitLow` (`packed = high * 256 + low`):

=== "Hub schema (v3)"

    ```yaml
    spec:
      properties:
        packed:
          type: integer
    ```

=== "Spoke schema (v1)"

    ```yaml
    spec:
      properties:
        bitHigh:
          type: integer
        bitLow:
          type: integer
    ```

### Rule

```yaml
- strategy: CEL
  cel:
    hubPaths:
      - spec.packed
    spokePaths:
      - spec.bitHigh
      - spec.bitLow
    hubToSpoke: '{"spec.bitHigh": int(object.spec.packed) / 256, "spec.bitLow": int(object.spec.packed) % 256}'
    spokeToHub: '{"spec.packed": int(object.spec.bitHigh) * 256 + int(object.spec.bitLow)}'
  acknowledgeLossy: true
  reason: >-
    integer packing via CEL is an escape hatch the engine cannot prove lossless.
```

### Objects

=== "Hub (v3)"

    ```yaml
    spec:
      packed: 1025
    ```

=== "Spoke (v1)"

    ```yaml
    spec:
      bitHigh: 4
      bitLow: 1
    ```
