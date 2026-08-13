# Type Coerce

## What it does

Converts a scalar field's JSON type — string, integer, number, or boolean — between whatever the hub and spoke schemas each declare at the same path.

## When to use it

The same field, same path, same meaning, is typed differently between versions — e.g. a `priority` field that was a `string` in one version and became an `integer` in another (a common cleanup once an API's real constraints become clear).

!!! lossless "Always lossless for whole values"
    Canonically-formatted whole values round-trip exactly in both directions. A value that genuinely can't be parsed as the target type (a non-numeric string coerced to a number) is a **runtime conversion error**, not a lossiness concern — it means the input object doesn't actually match its own declared schema.

    Fractional numbers written into an **integer** destination follow `onFractionalInteger` (default `Error`). `Truncate` and `Round` require `acknowledgeLossy: true` because they discard precision.

## Truncation matrix

JSON numbers decode as float64 in Go. The interesting cases are therefore "what happens when that float lands on a typed destination":

| Source value | Dest schema | `onFractionalInteger` | Result |
|---|---|---|---|
| `5` / `"5"` / `5.0` | integer | any | `5` |
| `1.7` / `"1.7"` | integer | `Error` (default) | conversion **error** |
| `1.7` / `"1.7"` | integer | `Truncate` | `1` (lossy) |
| `1.7` / `"1.7"` | integer | `Round` | `2` (lossy) |
| `1.7` | number | n/a | `1.7` (unchanged) |
| `5` | string | n/a | `"5"` |
| `"true"` | boolean | n/a | `true` |
| `"nope"` | integer | any | conversion **error** (unparseable) |

`onFractionalInteger` has no effect when the destination is string, number, or boolean.

## Example

The hub's `priority` (an integer) is a string on the v2 spoke:

=== "Hub schema (v3)"

    ```yaml
    spec:
      properties:
        priority:
          type: integer
    ```

=== "Spoke schema (v2)"

    ```yaml
    spec:
      properties:
        priority:
          type: string
    ```

### Rule

```yaml
- strategy: TypeCoerce
  typeCoerce:
    path: spec.priority
    # onFractionalInteger: Error  # default; Truncate and Round are lossy
```

`path` is the same dotted path on both sides — `typeCoerce` only makes sense when the field itself isn't renamed, just its type.

### Objects

=== "Hub (v3)"

    ```yaml
    spec:
      priority: 5
    ```

=== "Spoke (v2)"

    ```yaml
    spec:
      priority: "5"
    ```

Numbers are formatted canonically (no unnecessary trailing zeros or exponent notation) so a round trip through both directions always reproduces the exact original value.
