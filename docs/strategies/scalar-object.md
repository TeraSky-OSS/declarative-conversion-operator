# Scalar ⇄ Object

Two mirrored strategies: **`scalarToObject`** (hub is a scalar, spoke wraps it in an object) and **`objectToScalar`** (hub is an object, spoke is the scalar extracted from it).

## What it does

Wraps a bare scalar value into an object under a named key, or unwraps it back out. Common when an API evolves a simple value into a small structured object (or the reverse — simplifying an over-engineered object down to a scalar).

## When to use it

A field is a plain scalar on one version and an object with (at least) one key holding that same value on the other.

!!! lossless "Usually lossless"
    Lossless in both directions **as long as the object side has no other keys** the scalar side can't represent. If the object has extra keys beyond the wrapped one, the direction that collapses object→scalar is lossy (those extra keys have nowhere to go) unless you supply `defaultsForOtherKeys` to reconstruct them deterministically on the way back.

## Example: `scalarToObject`

The hub's `replicaCount` (a bare integer) is wrapped as `replicas.count` on the v2 spoke:

=== "Hub schema (v3)"

    ```yaml
    spec:
      properties:
        replicaCount:
          type: integer
    ```

=== "Spoke schema (v2)"

    ```yaml
    spec:
      properties:
        replicas:
          type: object
          properties:
            count:
              type: integer
    ```

### Rule

```yaml
- strategy: ScalarToObject
  scalarToObject:
    hubPath: spec.replicaCount
    spokePath: spec.replicas
    key: count
```

### Objects

=== "Hub (v3)"

    ```yaml
    spec:
      replicaCount: 3
    ```

=== "Spoke (v2)"

    ```yaml
    spec:
      replicas:
        count: 3
    ```

## Example: `objectToScalar`

The mirror image — the hub's `network` object is unwrapped to a bare `networkCIDR` string on the v2 spoke:

=== "Hub schema (v3)"

    ```yaml
    spec:
      properties:
        network:
          type: object
          properties:
            cidr:
              type: string
    ```

=== "Spoke schema (v2)"

    ```yaml
    spec:
      properties:
        networkCIDR:
          type: string
    ```

### Rule

```yaml
- strategy: ObjectToScalar
  objectToScalar:
    hubPath: spec.network
    spokePath: spec.networkCIDR
    key: cidr
```

### Objects

=== "Hub (v3)"

    ```yaml
    spec:
      network:
        cidr: "10.0.0.0/16"
    ```

=== "Spoke (v2)"

    ```yaml
    spec:
      networkCIDR: "10.0.0.0/16"
    ```

## Handling extra keys with `defaultsForOtherKeys`

If the object side has more keys than just the wrapped one, provide defaults so the reverse direction (object → scalar → object) can reconstruct them deterministically instead of silently dropping them:

```yaml
- strategy: ScalarToObject
  scalarToObject:
    hubPath: spec.version
    spokePath: spec.version
    key: major
    defaultsForOtherKeys:
      channel: "stable"
```

Here, converting hub `spec.version: "15"` down to the spoke produces `spec.version: {major: "15", channel: "stable"}`. Converting back up only reads `major` — if the spoke object's `channel` was ever changed from `"stable"`, that edit is lost on the way back up, which is why this configuration requires `acknowledgeLossy: true`.
