# Duration

## What it does

Converts between a Go duration string (`"5m"`, `"1h30m"`) and an integer number of seconds. Compile infers which side is the string from the schemas. Parsing uses [`time.ParseDuration`](https://pkg.go.dev/time#ParseDuration).

## When to use it

A timeout, interval, or TTL stored as a duration string on one API version and as integer seconds on another. This is not Kubernetes `resource.Quantity` — `"5m"` here is five minutes, not five millicores. Use [Quantity](quantity.md) for resource amounts.

!!! conditional-lossy "Integer seconds → canonical duration string is lossy"
    Parsing a duration string into seconds is exact (sub-second fractions are truncated toward zero). Writing those seconds back out uses Go's canonical `Duration.String()` spelling (`"5m0s"`), which is not always the original string (`"5m"`). That direction requires `acknowledgeLossy: true`.

## Example

The hub's `timeout` duration string is stored as seconds on the v1 spoke:

=== "Hub schema (v3)"

    ```yaml
    spec:
      properties:
        timeout:
          type: string
    ```

=== "Spoke schema (v1)"

    ```yaml
    spec:
      properties:
        timeoutSeconds:
          type: integer
    ```

### Rule

```yaml
- strategy: Duration
  duration:
    hubPath: spec.timeout
    spokePath: spec.timeoutSeconds
  acknowledgeLossy: true
  reason: >-
    integer seconds re-format as Go's canonical duration string (5m0s),
    which may differ from an equivalent spelling such as 5m.
```

### Objects

=== "Hub (v3)"

    ```yaml
    spec:
      timeout: "5m"
    ```

=== "Spoke (v1)"

    ```yaml
    spec:
      timeoutSeconds: 300
    ```

Converting `300` seconds back to the hub yields `"5m0s"`, not `"5m"`. That is the acknowledged-lossy direction.
