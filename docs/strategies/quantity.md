# Quantity

## What it does

Converts between a Kubernetes [`resource.Quantity`](https://pkg.go.dev/k8s.io/apimachinery/pkg/api/resource#Quantity) string (`"500m"`, `"2"`, `"1Gi"`) and an integer millivalue (`Quantity.MilliValue()`). Compile infers which side is the string from the schemas.

## When to use it

A field that stores a resource request or limit as a Quantity string on one API version and as an integer number of millicores (or milli-units) on another. `NumericScale` only rescales numbers; it cannot parse `"500m"`.

!!! conditional-lossy "Integer → canonical Quantity string is lossy"
    Parsing a Quantity string into a millivalue is exact. Writing that millivalue back out uses the canonical Quantity spelling (`"500m"`), which is not always the original string (`"0.5"` and `"500m"` are the same Quantity). That direction requires `acknowledgeLossy: true`.

## Example

The hub's `cpuRequest` Quantity string is stored as millicores on the v1 spoke:

=== "Hub schema (v3)"

    ```yaml
    spec:
      properties:
        cpuRequest:
          type: string
    ```

=== "Spoke schema (v1)"

    ```yaml
    spec:
      properties:
        cpuMillis:
          type: integer
    ```

### Rule

```yaml
- strategy: Quantity
  quantity:
    hubPath: spec.cpuRequest
    spokePath: spec.cpuMillis
  acknowledgeLossy: true
  reason: >-
    integer millivalues re-format as the canonical Quantity string (500m),
    which may differ from an equivalent spelling such as 0.5.
```

### Objects

=== "Hub (v3)"

    ```yaml
    spec:
      cpuRequest: "500m"
    ```

=== "Spoke (v1)"

    ```yaml
    spec:
      cpuMillis: 500
    ```

`"500m"` happens to be canonical, so this particular sample round-trips. A hub value of `"0.5"` converts to the same `500` millicores and comes back as `"500m"` — that is the acknowledged-lossy direction, and [`convctl test`](../cli.md) is how you see it on real data.
