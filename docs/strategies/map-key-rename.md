# Map Key Rename

## What it does

Renames known keys inside a free-form map (`additionalProperties`) and copies every other key through unchanged. The whole map is claimed, so coverage treats the unrenamed remainder as intentional passthrough — not an uncovered-field omission.

## When to use it

A map field kept its shape across versions, but one (or a few) well-known keys were renamed — `app` became `application`, while arbitrary extra keys must survive both directions. `FieldsToMap` / `MapToFields` need a closed set of keys; this strategy does not.

!!! lossless "Lossless when renames are injective"
    Compile rejects two hub keys mapping to the same spoke key. With a 1:1 rename table, both directions round-trip: known keys rename and reverse-rename, unknown keys pass through under the same name. A runtime collision (an unmapped source key that already equals a rename destination) is a conversion error, not silent data loss.

## Example

The hub's `extraLabels` map keeps the same path on v1, but the `app` key is spelled `application`:

=== "Hub schema (v3)"

    ```yaml
    spec:
      properties:
        extraLabels:
          type: object
          additionalProperties:
            type: string
    ```

=== "Spoke schema (v1)"

    ```yaml
    spec:
      properties:
        extraLabels:
          type: object
          additionalProperties:
            type: string
    ```

### Rule

```yaml
- strategy: MapKeyRename
  mapKeyRename:
    hubPath: spec.extraLabels
    spokePath: spec.extraLabels
    renames:
      app: application
```

`hubPath` and `spokePath` may differ if the map itself also moved; `renames` is always hub-key → spoke-key.

### Objects

=== "Hub (v3)"

    ```yaml
    spec:
      extraLabels:
        app: widget
        region: us-east-1
    ```

=== "Spoke (v1)"

    ```yaml
    spec:
      extraLabels:
        application: widget
        region: us-east-1
    ```
