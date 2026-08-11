# Enum Remap

## What it does

Bidirectionally maps a scalar field's enumerated values between what the hub calls them and what the spoke calls them — e.g. a hub `"Large"` that a spoke calls `"L"`.

## When to use it

The *same field, same meaning*, has different string enum values on different versions — a common cosmetic API cleanup (`"Small"/"Medium"/"Large"` becoming `"S"/"M"/"L"`, or vice versa).

!!! conditional-lossy "Depends on `onUnmappedHubValue`/`onUnmappedSpokeValue`, and whether the mapping is injective"
    Each direction is lossless only if every value that direction might encounter has an explicit mapping entry (`onUnmapped...Value: Error`, the default, fails validation otherwise — never silently passes an unknown value through). The **spoke → hub** direction has an extra condition: it's only lossless if the mapping is *injective* — no two hub values map to the same spoke value. If two hub values collapse to one spoke value, converting that spoke value back up is inherently ambiguous, and that direction requires `acknowledgeLossy: true`.

## Example

The hub's `size` enum values (`Small`/`Medium`/`Large`) map to the v2 spoke's abbreviated equivalents:

=== "Hub schema (v3)"

    ```yaml
    spec:
      properties:
        size:
          type: string
          enum: ["Small", "Medium", "Large"]
    ```

=== "Spoke schema (v2)"

    ```yaml
    spec:
      properties:
        size:
          type: string
          enum: ["S", "M", "L"]
    ```

### Rule

```yaml
- strategy: EnumRemap
  enumRemap:
    path: spec.size
    mapping:
      - hub: Small
        spoke: S
      - hub: Medium
        spoke: M
      - hub: Large
        spoke: L
```

`path` is the same dotted path on both sides — `enumRemap` only makes sense when the field itself isn't renamed, just its values.

### Objects

=== "Hub (v3)"

    ```yaml
    spec:
      size: "Large"
    ```

=== "Spoke (v2)"

    ```yaml
    spec:
      size: "L"
    ```

## Unmapped-value policy

```yaml
- strategy: EnumRemap
  enumRemap:
    path: spec.size
    mapping: [...]
    onUnmappedHubValue: Error    # default; Drop is also available
    onUnmappedSpokeValue: Error  # default
```

`Error` (the default, matching the fail-closed posture used throughout) rejects the configuration outright if either schema's declared `enum` values aren't all covered by `mapping`. Only set `Drop` if you deliberately want an unmapped value to disappear rather than fail conversion — this makes that direction lossy and requires `acknowledgeLossy: true`.
