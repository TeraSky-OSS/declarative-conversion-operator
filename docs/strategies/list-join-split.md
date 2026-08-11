# List Join / Split

Two mirrored strategies: **`listJoin`** (hub array of scalars → spoke delimited string) and **`listSplit`** (hub delimited string → spoke array of scalars). Functionally identical — which name you use just depends on which side is the array in your schemas.

## What it does

Converts an array of scalars into a single string by joining its (string-coerced) elements with a separator, and splits that string back into an array on the reverse direction.

## When to use it

An older (or simpler) API version stores a list as a single delimited string (`"8.8.8.8,1.1.1.1"`), while a properly-typed version uses a real array.

!!! lossless "Always lossless — with one caveat"
    Both directions round-trip exactly, **provided no element of the array contains the separator character as a substring**. If it does, joining and re-splitting won't reproduce the original array shape — `convctl test` correctly surfaces this as a genuine data problem for whatever sample triggered it, not as an expected characteristic of the strategy to acknowledge in the config.

## Example: `listJoin`

The hub's `dnsServers` array becomes a comma-joined string on the v2 spoke:

=== "Hub schema (v3)"

    ```yaml
    spec:
      properties:
        dnsServers:
          type: array
          items:
            type: string
    ```

=== "Spoke schema (v2)"

    ```yaml
    spec:
      properties:
        dnsServersCSV:
          type: string
    ```

### Rule

```yaml
- strategy: ListJoin
  listJoin:
    hubPath: spec.dnsServers
    spokePath: spec.dnsServersCSV
    separator: ","
```

### Objects

=== "Hub (v3)"

    ```yaml
    spec:
      dnsServers:
        - "8.8.8.8"
        - "1.1.1.1"
    ```

=== "Spoke (v2)"

    ```yaml
    spec:
      dnsServersCSV: "8.8.8.8,1.1.1.1"
    ```

## Example: `listSplit`

The mirror image, with the roles of hub and spoke reversed — the hub holds the delimited string, the spoke holds the array:

=== "Hub schema (v3)"

    ```yaml
    spec:
      properties:
        allowedCIDRsCSV:
          type: string
    ```

=== "Spoke schema (v1)"

    ```yaml
    spec:
      properties:
        allowedCIDRs:
          type: array
          items:
            type: string
    ```

### Rule

```yaml
- strategy: ListSplit
  listSplit:
    hubPath: spec.allowedCIDRsCSV
    spokePath: spec.allowedCIDRs
    separator: ","
```

### Objects

=== "Hub (v3)"

    ```yaml
    spec:
      allowedCIDRsCSV: "10.0.0.0/8,192.168.0.0/16"
    ```

=== "Spoke (v1)"

    ```yaml
    spec:
      allowedCIDRs:
        - "10.0.0.0/8"
        - "192.168.0.0/16"
    ```

`listJoin` and `listSplit` are the same underlying operation; use whichever name matches which side (hub or spoke) is the array in your particular schema pair.
