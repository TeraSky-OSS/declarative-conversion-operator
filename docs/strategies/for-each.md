# For Each

## What it does

Applies a nested list of ordinary conversion rules to each element of a hub array and the corresponding spoke array — the same rule vocabulary, just scoped to one array element at a time, with paths in the nested rules relative to a single element rather than the whole object.

## When to use it

Both sides have an array of objects at the same conceptual position, but the *shape of each element* differs between versions (a field renamed inside each array element, for instance) — `forEach` lets you describe that per-element mapping once instead of hand-writing index-specific rules.

!!! conditional-lossy "Depends entirely on the nested rules"
    `forEach` itself introduces no lossiness — the losslessness of a `forEach` rule is exactly the combined losslessness of its nested rules, applied per element. A `forEach` wrapping only `fieldRename` rules (as below) is fully lossless in both directions.

## Example

Each element of the hub's `volumes` array renames `sizeGB`/`label` to the v1 spoke's `size`/`name`:

=== "Hub schema (v3)"

    ```yaml
    spec:
      properties:
        volumes:
          type: array
          items:
            type: object
            properties:
              sizeGB:
                type: string
              label:
                type: string
    ```

=== "Spoke schema (v1)"

    ```yaml
    spec:
      properties:
        volumes:
          type: array
          items:
            type: object
            properties:
              size:
                type: string
              name:
                type: string
    ```

### Rule

```yaml
- strategy: ForEach
  forEach:
    hubItemsPath: spec.volumes
    spokeItemsPath: spec.volumes
    rules:
      - strategy: FieldRename
        fieldRename:
          hubPath: sizeGB      # relative to one array element
          spokePath: size
      - strategy: FieldRename
        fieldRename:
          hubPath: label
          spokePath: name
```

Note that the nested rules' `hubPath`/`spokePath` (`sizeGB`, `size`, `label`, `name`) are relative to a single array element — not prefixed with `spec.volumes[]`.

### Objects

=== "Hub (v3)"

    ```yaml
    spec:
      volumes:
        - sizeGB: "100"
          label: "data"
    ```

=== "Spoke (v1)"

    ```yaml
    spec:
      volumes:
        - size: "100"
          name: "data"
    ```

## Constraints

- **Nesting is capped at depth 1.** A `forEach`'s own rule list can't contain another `forEach` — see [Limitations](../limitations.md).
- **Strict positional correspondence is required at runtime.** The hub and spoke arrays must have the same length and element order; a length mismatch during an actual conversion is a hard runtime error, not a best-effort merge or truncation.
