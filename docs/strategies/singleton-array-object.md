# Singleton Array ⇄ Object

Two mirrored strategies: **`singletonArrayToObject`** (hub is an array, spoke is an object taken from the array's first element) and **`objectToSingletonArray`** (hub is an object, spoke is a single-element array wrapping it).

## What it does

Converts between "a list that in practice only ever has one element" and "a bare object" — a common API-evolution pattern when a field is generalized from singular to plural (or simplified back down).

## When to use it

One version models something as a list (anticipating multiple values in the future, or as a historical artifact), and the other models it as a single object, and in practice the list is never expected to hold more than one element.

!!! conditional-lossy "Lossless only if the array side declares `maxItems: 1`"
    Converting **object → array** is always lossless (wrapping a single value in a one-element array loses nothing). Converting **array → object** is lossless *only if the schema itself guarantees at most one element* (`maxItems: 1`) — otherwise a real array with 2+ elements would silently drop everything past the first, which the engine refuses to treat as lossless. Without `maxItems: 1` declared, this direction requires `acknowledgeLossy: true`.

## Example: `singletonArrayToObject`

The hub's `zones` array (capped at one element) becomes a bare `zone` object on the v1 spoke:

=== "Hub schema (v3)"

    ```yaml
    spec:
      properties:
        zones:
          type: array
          maxItems: 1   # <- makes array->object provably lossless
          items:
            type: object
            properties:
              name:
                type: string
    ```

=== "Spoke schema (v1)"

    ```yaml
    spec:
      properties:
        zone:
          type: object
          properties:
            name:
              type: string
    ```

### Rule

```yaml
- strategy: SingletonArrayToObject
  singletonArrayToObject:
    hubPath: spec.zones
    spokePath: spec.zone
```

### Objects

=== "Hub (v3)"

    ```yaml
    spec:
      zones:
        - name: "us-east-1a"
    ```

=== "Spoke (v1)"

    ```yaml
    spec:
      zone:
        name: "us-east-1a"
    ```

## Example: `objectToSingletonArray`

The mirror image — the hub's `primaryRegion` object becomes a one-element `regions` array on the v1 spoke:

=== "Hub schema (v3)"

    ```yaml
    spec:
      properties:
        primaryRegion:
          type: object
          properties:
            name:
              type: string
    ```

=== "Spoke schema (v1)"

    ```yaml
    spec:
      properties:
        regions:
          type: array
          maxItems: 1
          items:
            type: object
            properties:
              name:
                type: string
    ```

### Rule

```yaml
- strategy: ObjectToSingletonArray
  objectToSingletonArray:
    hubPath: spec.primaryRegion
    spokePath: spec.regions
```

### Objects

=== "Hub (v3)"

    ```yaml
    spec:
      primaryRegion:
        name: "us-east-1"
    ```

=== "Spoke (v1)"

    ```yaml
    spec:
      regions:
        - name: "us-east-1"
    ```
