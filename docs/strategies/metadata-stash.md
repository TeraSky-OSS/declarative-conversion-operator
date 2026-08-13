# To Annotation / Label / From Annotation / Label

Four closely related strategies for moving a schema field into metadata (or
back). **`toAnnotation`** / **`toLabel`** stash a hub field into spoke
metadata. **`fromAnnotation`** / **`fromLabel`** are the inverse geometry:
the schema field lives on the spoke, and hub metadata holds the stash key.

## What `toAnnotation` / `toLabel` do

Move a hub-only field's value into a metadata annotation or label on the spoke
object, rather than dropping it outright. With `restoreOnReverse: true`,
converting back up reads the stashed value back out, making the round-trip
lossless.

## When to use `toAnnotation` / `toLabel`

A field exists on the hub but genuinely has no equivalent field in a spoke
version's schema — but you still want its value preserved somewhere
recoverable, rather than lost, when an object is read/written at that spoke
version. `toLabel` additionally makes the value usable in label selectors on
the spoke version.

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

## What `fromAnnotation` / `fromLabel` do

The geometric inverse of `to*` after a hub promotion (or whenever the schema
field lives on the spoke and the stash key lives on the hub). Hub → spoke
restores the spoke field from hub metadata; with `stashOnReverse: true`,
spoke → hub stashes the spoke field back onto hub metadata.

!!! conditional-lossy "Lossless only with `stashOnReverse: true`"
    Hub → spoke (restoring from metadata) does not lose hub schema data — the
    field is not on the hub. Spoke → hub is lossless **only if `stashOnReverse: true`**;
    without it the spoke-only field is dropped when converting up.

## Example: `fromAnnotation`

```yaml
- strategy: FromAnnotation
  fromAnnotation:
    spokePath: spec.operatorNote
    key: xrd.example.org/operator-note
    stashOnReverse: true
```

## Example: `fromLabel`

```yaml
- strategy: FromLabel
  fromLabel:
    spokePath: spec.operatorTier
    key: operator-tier
    serialization: String
    stashOnReverse: true
```

`convctl rehub` maps `ToAnnotation` ↔ `FromAnnotation` and `ToLabel` ↔
`FromLabel` when rewriting a config around a new hub version.

## `serialization`

| Value | Behavior |
|---|---|
| `JSON` (default for annotations) | The value is JSON-encoded before being stored — safe for any scalar, object, or array value, at the cost of quoted strings and escaped characters in the raw annotation/label value. |
| `String` (required for labels) | The value is stored as-is with no encoding — only valid for values that are already plain strings (required for labels, which must be valid label values). |

`FromLabel` rejects `serialization: JSON` at admission and compile time (defaulting to `String` when unset). Writing a label also validates the value against Kubernetes label rules at conversion time.
