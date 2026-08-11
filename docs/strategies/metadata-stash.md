# To Annotation / Label

Two closely related strategies: **`toAnnotation`** and **`toLabel`**. Both stash a hub field's value into spoke `metadata` — an annotation or a label, respectively — since the field doesn't exist in the spoke's `spec`/`status` schema at all.

## What it does

Moves a hub-only field's value into a metadata annotation or label on the spoke object, rather than dropping it outright. With `restoreOnReverse: true`, converting back up reads the stashed value back out, making the round-trip lossless.

## When to use it

A field exists on the hub but genuinely has no equivalent field in a spoke version's schema — but you still want its value preserved somewhere recoverable, rather than lost, when an object is read/written at that spoke version. `toLabel` additionally makes the value usable in label selectors on the spoke version.

!!! conditional-lossy "Lossless only with `restoreOnReverse: true`"
    Hub → spoke (stashing the value) is always lossless. Spoke → hub is lossless **only if `restoreOnReverse: true`** — without it, a spoke-native object (created directly at the spoke version, never having gone through the hub) has no stashed value to restore, so the hub-side field would come back empty; that direction then requires `acknowledgeLossy: true`.

## Example: `toAnnotation`

The hub's `description` field is stashed into an annotation on the v2 spoke:

=== "Hub schema (v3)"

    ```yaml
    spec:
      properties:
        description:
          type: string
    ```

=== "Spoke schema (v2)"

    ```yaml
    # v2 has no "description" field in spec at all
    spec:
      properties: {}
    ```

### Rule

```yaml
- strategy: ToAnnotation
  toAnnotation:
    hubPath: spec.description
    key: xrd.example.org/description
    restoreOnReverse: true
```

### Objects

=== "Hub (v3)"

    ```yaml
    spec:
      description: "created via v3"
    ```

=== "Spoke (v2)"

    ```yaml
    metadata:
      annotations:
        # JSON-serialized by default (serialization: JSON) -- note the
        # embedded quotes, which are part of the stored string
        xrd.example.org/description: '"created via v3"'
    ```

## Example: `toLabel`

The hub's `tier` field is stashed into a label on the v1 spoke, using `serialization: String` (raw value, no JSON quoting — appropriate since labels must already be plain strings):

=== "Hub schema (v3)"

    ```yaml
    spec:
      properties:
        tier:
          type: string
    ```

### Rule

```yaml
- strategy: ToLabel
  toLabel:
    hubPath: spec.tier
    key: tier
    serialization: String
    restoreOnReverse: true
```

### Objects

=== "Hub (v3)"

    ```yaml
    spec:
      tier: "gold"
    ```

=== "Spoke (v1)"

    ```yaml
    metadata:
      labels:
        tier: "gold"
    ```

## `serialization`

| Value | Behavior |
|---|---|
| `JSON` (default) | The value is JSON-encoded before being stored — safe for any scalar, object, or array value, at the cost of quoted strings and escaped characters in the raw annotation/label value. |
| `String` | The value is stored as-is with no encoding — only valid for values that are already plain strings (required for labels, which must be valid label values). |
