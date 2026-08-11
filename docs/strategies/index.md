# Strategy Reference

Every `XRDConversionConfig` rule picks one of these built-in strategies. Each is designed with bidirectional semantics — hub→spoke and spoke→hub — and the engine determines losslessness for each direction independently. Any direction the engine can't prove round-trips exactly requires `acknowledgeLossy: true` on the rule, or the whole config is rejected.

The default posture is **fail-closed**: any hub or spoke field left unclaimed by a rule, and not structurally identical on both sides, is a validation error — never a silent drop. See [XRDConversionConfig](../configuration/xrdconversionconfig.md) for the full rule-authoring model.

## All strategies at a glance

| Strategy | What it does | Typically lossless? |
|---|---|---|
| [`fieldRename`](field-rename.md) | Renames a field, preserving its value and shape exactly. | :material-check-all:{ style="color:#2e7d32" } Always |
| [`scalarToObject`](scalar-object.md) | Hub scalar ⇄ spoke object wrapping it under a key. | :material-check:{ style="color:#2e7d32" } Usually |
| [`objectToScalar`](scalar-object.md) | Hub object ⇄ spoke scalar extracted from a key. | :material-check:{ style="color:#2e7d32" } Usually |
| [`singletonArrayToObject`](singleton-array-object.md) | Hub single-element array ⇄ spoke object. | :material-alert:{ style="color:#ef6c00" } Conditional |
| [`objectToSingletonArray`](singleton-array-object.md) | Hub object ⇄ spoke single-element array. | :material-alert:{ style="color:#ef6c00" } Conditional |
| [`fieldsToMap`](fields-map.md) | Several hub sibling fields ⇄ one spoke map. | :material-alert:{ style="color:#ef6c00" } Conditional |
| [`mapToFields`](fields-map.md) | Hub map ⇄ several spoke sibling fields. | :material-alert:{ style="color:#ef6c00" } Conditional |
| [`toAnnotation`](metadata-stash.md) | Stashes a hub field's value into a spoke annotation. | :material-alert:{ style="color:#ef6c00" } Conditional (`restoreOnReverse`) |
| [`toLabel`](metadata-stash.md) | Stashes a hub field's value into a spoke label. | :material-alert:{ style="color:#ef6c00" } Conditional (`restoreOnReverse`) |
| [`enumRemap`](enum-remap.md) | Bidirectionally maps a scalar field's enumerated values. | :material-alert:{ style="color:#ef6c00" } Conditional |
| [`defaultValue`](default-value.md) | Injects a default for a field that only exists on one side. | :material-close:{ style="color:#c62828" } One direction always lossy |
| [`constant`](constant.md) | Forces a field to a fixed value on the side it exists on. | :material-close:{ style="color:#c62828" } One direction always lossy |
| [`delete`](delete.md) | Intentionally drops a field that exists on only one side. | :material-close:{ style="color:#c62828" } Always requires acknowledgement |
| [`jsonPatch`](json-patch.md) | Escape hatch: raw RFC 6902 JSON Patch per direction. | :material-close:{ style="color:#c62828" } Lossy unless `losslessOverride` |
| [`forEach`](for-each.md) | Applies a nested rule list to each element of an array. | Depends on nested rules |
| [`typeCoerce`](type-coerce.md) | Converts a scalar's JSON type (string/int/number/bool). | :material-check-all:{ style="color:#2e7d32" } Always |
| [`scalarToFields`](scalar-fields.md) | Decomposes one scalar into several fields via regex + template. | :material-close:{ style="color:#c62828" } Lossy unless `losslessOverride` |
| [`fieldsToScalar`](scalar-fields.md) | Joins several fields into one scalar via template + regex. | :material-close:{ style="color:#c62828" } Lossy unless `losslessOverride` |
| [`arrayToMapByKey`](array-map-key.md) | Array of objects ⇄ map keyed by one of their fields. | :material-alert:{ style="color:#ef6c00" } Array→map yes, map→array no |
| [`mapToArrayByKey`](array-map-key.md) | Map ⇄ array of objects keyed by one of their fields. | :material-alert:{ style="color:#ef6c00" } Map→array no, array→map yes |
| [`numericScale`](numeric-scale.md) | Rescales a numeric field by a fixed factor. | :material-alert:{ style="color:#ef6c00" } Conditional (integer side) |
| [`listJoin`](list-join-split.md) | Array of scalars ⇄ delimited string. | :material-check-all:{ style="color:#2e7d32" } Always |
| [`listSplit`](list-join-split.md) | Delimited string ⇄ array of scalars. | :material-check-all:{ style="color:#2e7d32" } Always |

## Reading the examples

Every strategy page follows the same shape:

1. **What it does** and **when to use it**.
2. **Lossy/lossless semantics**, called out explicitly per direction.
3. An **XRD schema snippet** showing the field on the hub version and the corresponding field on a spoke version.
4. The **`XRDConversionConfig` rule** that maps between them.
5. **Example objects** at both versions, so you can see the exact before/after shape.

All examples are self-contained and runnable through `convctl test` — see [Getting Started](../getting-started.md) and the [CLI Reference](../cli.md). For a single fixture exercising every strategy in this reference at once, see [`internal/cli/testdata/full`](https://github.com/terasky-oss/declarative-conversion-operator/tree/main/internal/cli/testdata/full) in the repository.
