# Constant

## What it does

Forces a field that exists on only one side to a fixed, hard-coded value — not derived from anything on the other side. The direction converting *away from* the side that has it drops the real value, exactly like [Default Value](default-value.md), except the injected value is always the same constant rather than a schema default.

## When to use it

A field is a bookkeeping/versioning marker that should always read a specific fixed value at a given version, regardless of what (if anything) was ever set — a schema-version tag being the canonical example.

!!! conditional-lossy "One direction is always lossy"
    Same shape as [Default Value](default-value.md): the direction that injects the constant is lossless (nothing real is being overwritten with useful information — it's a marker), and the direction converting away from the side that has it drops whatever real value was there, which is always lossy and always requires `acknowledgeLossy: true`.

## Example

`schemaVersion` exists only on the v2 spoke, and is always forced to the literal string `"v2"` regardless of anything else:

=== "Hub schema (v3)"

    ```yaml
    spec:
      properties:
        # no schemaVersion field
        storageGB:
          type: string
    ```

=== "Spoke schema (v2)"

    ```yaml
    spec:
      properties:
        schemaVersion:
          type: string
    ```

### Rule

```yaml
- strategy: Constant
  constant:
    path: spec.schemaVersion
    existsOn: Spoke
    value: "v2"
  acknowledgeLossy: true
  reason: >-
    schemaVersion is a v2-only bookkeeping field; its value is always
    forced to "v2" and carries no information worth round-tripping
    through the hub.
```

### Objects

=== "Hub (v3)"

    ```yaml
    spec:
      storageGB: "500"
      # (no schemaVersion — the hub schema doesn't have this field)
    ```

=== "Spoke (v2)"

    ```yaml
    spec:
      storageSize: "500"
      schemaVersion: "v2"   # <- always this constant, regardless of input
    ```

Even if a v2-native object is created with `schemaVersion: "some-other-value"`, converting up to the hub and back down always yields `schemaVersion: "v2"` — the original value is discarded, matching the acknowledged-lossy direction.
