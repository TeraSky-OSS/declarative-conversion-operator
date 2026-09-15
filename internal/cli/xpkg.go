/*
Copyright 2026 The declarative-conversion-operator Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package cli

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	sigsyaml "sigs.k8s.io/yaml"
)

// packageFile is the path, inside a package layer, of the multi-document
// stream carrying the package's meta object and everything it ships.
const packageFile = "package.yaml"

// maxPackageBytes bounds what is read out of a package layer. A package is
// a file somebody hands this tool; a decompression bomb should fail as a
// too-large package rather than as an exhausted machine.
const maxPackageBytes = 256 << 20 // 256 MiB

// PackageContents is what a Crossplane package declares.
type PackageContents struct {
	// Ref is the package as the user named it, for error messages.
	Ref string
	// Meta is the package's own meta object (kind Configuration,
	// Provider, Function), when it has one.
	Meta *unstructured.Unstructured
	// XRDs are the CompositeResourceDefinitions the package ships, in the
	// order the package declares them.
	XRDs []*unstructured.Unstructured
	// Others counts documents that are neither, so "no XRDs in package"
	// can distinguish an empty package from one full of other things.
	Others int
}

// XRDNames lists the XRDs a package ships, for an error that has to name
// the candidates.
func (p *PackageContents) XRDNames() []string {
	out := make([]string, 0, len(p.XRDs))
	for _, x := range p.XRDs {
		out = append(out, xrdName(x))
	}
	sort.Strings(out)
	return out
}

// SelectXRD returns the one XRD to work against.
//
// With one XRD the choice is made. With several, --target has to name one:
// silently picking the first would make the answer depend on the order the
// package happened to be built in.
func (p *PackageContents) SelectXRD(target string) (*unstructured.Unstructured, error) {
	switch {
	case len(p.XRDs) == 0:
		if p.Others > 0 {
			return nil, fmt.Errorf("%s contains no CompositeResourceDefinitions (it does contain %d other object(s)); a conversion config needs an XRD to check against", p.Ref, p.Others)
		}
		return nil, fmt.Errorf("%s contains no objects at all", p.Ref)
	case target != "":
		for _, x := range p.XRDs {
			if xrdName(x) == target {
				return x, nil
			}
		}
		return nil, fmt.Errorf("%s ships no XRD named %q; it ships: %s", p.Ref, target, strings.Join(p.XRDNames(), ", "))
	case len(p.XRDs) == 1:
		return p.XRDs[0], nil
	default:
		return nil, fmt.Errorf("%s ships %d XRDs, so --target must name one of: %s",
			p.Ref, len(p.XRDs), strings.Join(p.XRDNames(), ", "))
	}
}

// ReadPackage reads a Crossplane package.
//
// Only the local form is implemented: a .xpkg file, which is an OCI image
// saved as a tarball, readable with the standard library alone. That is the
// tightest loop — "does my config hold against the XRDs I am about to
// publish?" — and it costs no new dependency. The registry and cluster
// forms are named in the error rather than silently unsupported.
func ReadPackage(ref string) (*PackageContents, error) {
	switch {
	case strings.HasPrefix(ref, "configuration/"), strings.HasPrefix(ref, "configurationrevision/"):
		return nil, fmt.Errorf("reading a package from the cluster (%s) is not implemented yet; build or pull the package and pass the .xpkg file", ref)
	case looksLikeImageRef(ref):
		return nil, fmt.Errorf("reading a package from a registry (%s) is not implemented yet; `crossplane xpkg pull %s -o package.xpkg` and pass the file", ref, ref)
	}
	return readLocalXPKG(ref)
}

// looksLikeImageRef distinguishes ghcr.io/org/platform:v1 from ./p.xpkg.
//
// Deliberately narrow: anything that is not clearly a registry reference is
// treated as a path, so a missing or mistyped file fails with "reading
// package: no such file" rather than with advice about pulling an image.
func looksLikeImageRef(ref string) bool {
	if _, err := os.Stat(ref); err == nil {
		return false // it exists; it is a file
	}
	if strings.HasSuffix(ref, ".xpkg") || strings.HasPrefix(ref, ".") || strings.HasPrefix(ref, "/") {
		return false
	}
	host, rest, ok := strings.Cut(ref, "/")
	if !ok || rest == "" {
		return false // a registry reference for a package always has a path
	}
	return strings.Contains(host, ".") || strings.Contains(host, ":") || host == "localhost"
}

// readLocalXPKG reads the package stream out of a .xpkg file.
//
// An xpkg is what `docker save` produces: a tar containing manifest.json,
// a config blob, and one gzipped tar per layer. The package's contents are
// package.yaml inside one of those layers — the last one that has it, since
// a later layer overwrites an earlier one.
func readLocalXPKG(path string) (*PackageContents, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, fmt.Errorf("reading package %s: %w", path, err)
	}
	if info.IsDir() {
		return nil, fmt.Errorf("%s is a directory; --package takes a .xpkg file, an image reference, or a cluster reference", path)
	}

	layers, err := xpkgLayerOrder(path)
	if err != nil {
		return nil, err
	}

	var data []byte
	for _, layer := range layers {
		got, err := readFileFromLayer(path, layer, packageFile)
		if err != nil {
			return nil, err
		}
		if got != nil {
			data = got // later layers win
		}
	}
	if data == nil {
		return nil, fmt.Errorf("%s is not a Crossplane package: no %s in any layer", path, packageFile)
	}
	return parsePackageStream(path, data)
}

// dockerManifestEntry is the subset of manifest.json this needs.
type dockerManifestEntry struct {
	Layers []string `json:"Layers"`
}

// xpkgLayerOrder reads the layer order from manifest.json, so "last layer
// wins" means what the image says rather than what the tar happened to
// list.
func xpkgLayerOrder(path string) ([]string, error) {
	raw, err := readFileFromTar(path, "manifest.json")
	if err != nil {
		return nil, err
	}
	if raw == nil {
		return nil, fmt.Errorf("%s is not a package: no manifest.json (expected the output of `crossplane xpkg build`)", path)
	}
	var entries []dockerManifestEntry
	if err := json.Unmarshal(raw, &entries); err != nil {
		return nil, fmt.Errorf("%s: parsing manifest.json: %w", path, err)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("%s: manifest.json declares no image", path)
	}
	return entries[0].Layers, nil
}

// readFileFromTar returns one member of a tar, or nil when it is absent.
func readFileFromTar(archive, name string) ([]byte, error) {
	// #nosec G304 -- the path the user passed to --package, same trust as
	// --config and --xrd.
	f, err := os.Open(archive)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", archive, err)
	}
	defer func() { _ = f.Close() }()

	tr := tar.NewReader(f)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("%s is not a readable tar archive: %w", archive, err)
		}
		if hdr.Name != name {
			continue
		}
		return readBounded(tr, archive)
	}
}

// readFileFromLayer returns one member of one (gzipped) layer, or nil when
// either the layer or the member is absent.
func readFileFromLayer(archive, layer, name string) ([]byte, error) {
	// #nosec G304 -- see readFileFromTar.
	f, err := os.Open(archive)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", archive, err)
	}
	defer func() { _ = f.Close() }()

	tr := tar.NewReader(f)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil, nil
		}
		if err != nil {
			return nil, fmt.Errorf("%s is not a readable tar archive: %w", archive, err)
		}
		if hdr.Name != layer {
			continue
		}
		var inner io.Reader = tr
		if strings.HasSuffix(layer, ".gz") {
			gz, gerr := gzip.NewReader(tr)
			if gerr != nil {
				return nil, fmt.Errorf("%s: layer %s is not gzip: %w", archive, layer, gerr)
			}
			defer func() { _ = gz.Close() }()
			inner = gz
		}
		ltr := tar.NewReader(inner)
		for {
			lhdr, lerr := ltr.Next()
			if errors.Is(lerr, io.EOF) {
				return nil, nil
			}
			if lerr != nil {
				return nil, fmt.Errorf("%s: reading layer %s: %w", archive, layer, lerr)
			}
			if strings.TrimPrefix(lhdr.Name, "./") != name {
				continue
			}
			return readBounded(ltr, archive)
		}
	}
}

// readBounded reads at most maxPackageBytes, so a package that decompresses
// to something enormous fails as a too-large package rather than as an
// exhausted machine.
func readBounded(r io.Reader, archive string) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxPackageBytes+1))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", archive, err)
	}
	if len(data) > maxPackageBytes {
		return nil, fmt.Errorf("%s: package contents exceed the %d MiB read limit", archive, maxPackageBytes>>20)
	}
	return data, nil
}

// parsePackageStream splits the multi-document package stream and keeps the
// meta object and the XRDs.
func parsePackageStream(ref string, data []byte) (*PackageContents, error) {
	out := &PackageContents{Ref: ref}
	for _, doc := range splitYAML(data) {
		var m map[string]any
		if err := sigsyaml.Unmarshal(doc, &m); err != nil {
			// One unparseable document does not invalidate a package, and
			// a package this tool cannot fully read is still usable for
			// the XRDs it can.
			continue
		}
		if len(m) == 0 {
			continue
		}
		u := &unstructured.Unstructured{Object: m}
		apiVersion, kind := u.GetAPIVersion(), u.GetKind()
		switch {
		case strings.HasPrefix(apiVersion, "meta.pkg.crossplane.io/"):
			out.Meta = u
		case kind == "CompositeResourceDefinition" && strings.HasPrefix(apiVersion, "apiextensions.crossplane.io/"):
			out.XRDs = append(out.XRDs, u)
		default:
			out.Others++
		}
	}
	if out.Meta == nil && len(out.XRDs) == 0 && out.Others == 0 {
		return nil, fmt.Errorf("%s: %s is empty", ref, packageFile)
	}
	return out, nil
}

// splitYAML splits a multi-document stream on document separators.
func splitYAML(data []byte) [][]byte {
	var out [][]byte
	for _, part := range strings.Split(string(data), "\n---") {
		trimmed := strings.TrimSpace(strings.TrimPrefix(part, "---"))
		if trimmed == "" {
			continue
		}
		out = append(out, []byte(trimmed))
	}
	return out
}

// XRDFromSource resolves an XRD from whichever schema source the caller
// named: a file, or a package plus an optional target.
//
// --package slots in exactly where --xrd does rather than being a new verb,
// which is what makes validate, analyze, test, diff and lint all gain it at
// once.
func XRDFromSource(xrdPath, packageRef, target string) (*unstructured.Unstructured, error) {
	if packageRef == "" {
		return LoadXRD(xrdPath)
	}
	pkg, err := ReadPackage(packageRef)
	if err != nil {
		return nil, err
	}
	return pkg.SelectXRD(target)
}
