# Array ⇄ Map by Key

Two mirrored strategies: **`arrayToMapByKey`** (hub is an array of objects, spoke is a map keyed by one of their fields) and **`mapToArrayByKey`** (hub is the map, spoke is the array).

## What it does

Converts between the standard "list-map" API-evolution pattern (an array of objects, one of whose fields acts as a de facto unique key) and an actual map keyed by that field — `keyField` itself becomes the map key and is not duplicated into the map's value.

## When to use it

An array of objects is effectively keyed by one of its fields (names must be unique in practice) on one version, and a later — or earlier — version models that as a real map instead, which is both more idiomatic and avoids needing a separate uniqueness check.

!!! conditional-lossy "Array→map is lossless; map→array is always lossy"
    **Array → map** is lossless: a duplicate key value across array elements is a **hard runtime conversion error** (never a silent overwrite), so a successful conversion is guaranteed to preserve everything. **Map → array** is always treated as lossy: the reconstructed array is emitted **sorted by key**, which doesn't necessarily reproduce whatever order the original array had before it was ever converted to a map — this direction always requires `acknowledgeLossy: true`.

## Example: `arrayToMapByKey`

The hub's `endpoints` array (each keyed by `name`) becomes a map on the v1 spoke:

=== "Hub schema (v3)"

    ```yaml
    spec:
      properties:
        endpoints:
          type: array
          items:
            type: object
            properties:
              name:
                type: string
              url:
                type: string
    ```

=== "Spoke schema (v1)"

    ```yaml
    spec:
      properties:
        endpoints:
          type: object
          additionalProperties:
            type: object
            properties:
              url:
                type: string
    ```

### Rule

```yaml
- strategy: ArrayToMapByKey
  arrayToMapByKey:
    hubPath: spec.endpoints
    spokePath: spec.endpoints
    keyField: name
  acknowledgeLossy: true
  reason: >-
    the array->map hub->spoke direction is lossless, but the reverse
    (spoke->hub) reconstructs the array sorted by key rather than
    preserving whatever order the original array had.
```

Note `keyField: name` is *not* duplicated into the map's value — only `url` remains once `name` becomes the map key.

### Objects

=== "Hub (v3)"

    ```yaml
    spec:
      endpoints:
        - name: "web"
          url: "https://web.example.com"
    ```

=== "Spoke (v1)"

    ```yaml
    spec:
      endpoints:
        web:
          url: "https://web.example.com"
    ```

## Example: `mapToArrayByKey`

The structural mirror — the hub's `limitsByTier` map becomes an array on the v1 spoke:

=== "Hub schema (v3)"

    ```yaml
    spec:
      properties:
        limitsByTier:
          type: object
          additionalProperties:
            type: object
            properties:
              limit:
                type: string
    ```

=== "Spoke schema (v1)"

    ```yaml
    spec:
      properties:
        tierLimits:
          type: array
          items:
            type: object
            properties:
              tier:
                type: string
              limit:
                type: string
    ```

### Rule

```yaml
- strategy: MapToArrayByKey
  mapToArrayByKey:
    hubPath: spec.limitsByTier
    spokePath: spec.tierLimits
    keyField: tier
  acknowledgeLossy: true
  reason: >-
    the map->array hub->spoke direction reconstructs the array sorted
    by key rather than preserving any particular order.
```

### Objects

=== "Hub (v3)"

    ```yaml
    spec:
      limitsByTier:
        gold:
          limit: "1000"
        silver:
          limit: "500"
    ```

=== "Spoke (v1)"

    ```yaml
    spec:
      tierLimits:
        - tier: "gold"      # sorted alphabetically: "gold" < "silver"
          limit: "1000"
        - tier: "silver"
          limit: "500"
    ```

Converting the array back up to the map (spoke → hub) is lossless — a map has no order to lose.
