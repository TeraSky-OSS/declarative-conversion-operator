# Type Coerce

## What it does

Converts a scalar field's JSON type — string, integer, number, or boolean — between whatever the hub and spoke schemas each declare at the same path.

## When to use it

The same field, same path, same meaning, is typed differently between versions — e.g. a `priority` field that was a `string` in one version and became an `integer` in another (a common cleanup once an API's real constraints become clear).

!!! lossless "Always lossless"
    Canonically-formatted values round-trip exactly in both directions. A value that genuinely can't be parsed as the target type (a non-numeric string coerced to a number) is a **runtime conversion error**, not a lossiness concern — it means the input object doesn't actually match its own declared schema.

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
