`platform.xpkg` is a real `crossplane xpkg build` output, not a hand-made
fixture: the reader's job is to agree with the format Crossplane actually
produces, and a fixture written from the same assumptions as the reader would
prove nothing.

It ships two XRDs (`xwidgets.example.org` and `xbuckets.example.org`), which
is what makes it exercise `--target` as well as the single-XRD path.

Rebuild with:

    crossplane xpkg build --package-root=<dir> --package-file=platform.xpkg

where `<dir>` holds a `crossplane.yaml` Configuration meta file plus the two
XRDs from `examples/`.
