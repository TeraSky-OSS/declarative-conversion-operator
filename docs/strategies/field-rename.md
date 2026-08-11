# Field Rename

## What it does

Renames a field from one path to another, preserving its value and shape exactly. The simplest and most common strategy — most real API version migrations are mostly this.

## When to use it

A field's meaning is unchanged between versions, only its name is different. If the field's *type* also needs to change (e.g. string → integer), see [Type Coerce](type-coerce.md) instead, or combine both in a `forEach` if it's inside an array.

!!! lossless "Always lossless"
    Both directions preserve the value exactly — nothing is computed, dropped, or defaulted.

## Example

The hub renamed `storageGB` from the spoke's `storageSize`:

=== "Hub schema (v2)"

    ```yaml
    spec:
      properties:
        storageGB:
          type: string
    ```

=== "Spoke schema (v1)"

    ```yaml
    spec:
      properties:
        storageSize:
          type: string
    ```

### Rule

```yaml
- strategy: FieldRename
  fieldRename:
    hubPath: spec.storageGB
    spokePath: spec.storageSize
```

### Objects

=== "Hub (v2)"

    ```yaml
    apiVersion: example.org/v2
    kind: XWidget
    spec:
      storageGB: "500"
    ```

=== "Spoke (v1)"

    ```yaml
    apiVersion: example.org/v1
    kind: XWidget
    spec:
      storageSize: "500"
    ```

`fieldRename` applies identically to `status.*` paths — see [XRDConversionConfig: status fields](../configuration/xrdconversionconfig.md#status-fields-work-exactly-like-spec-fields).
