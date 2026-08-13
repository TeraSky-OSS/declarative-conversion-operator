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
	"path/filepath"
	"testing"

	teraskyv1alpha1 "github.com/terasky-oss/declarative-conversion-operator/api/v1alpha1"
)

func TestRunRehub_CrossplaneStage02to03(t *testing.T) {
	t.Parallel()
	root := filepath.Join("..", "..", "examples", "crossplane-xr-multiversion")
	out, err := RunRehub(RehubOptions{
		ConfigPath: filepath.Join(root, "02-add-v2", "xrdconversionconfig.yaml"),
		XRDPath:    filepath.Join(root, "02-add-v2", "xrd.yaml"),
		To:         "v2",
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := out.(*teraskyv1alpha1.XRDConversionConfig)
	want, err := LoadConfig(filepath.Join(root, "03-promote-v2", "xrdconversionconfig.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	assertRehubMatches(t, cfg, want)
}

func TestRunRehub_CrossplaneStage04to05(t *testing.T) {
	t.Parallel()
	root := filepath.Join("..", "..", "examples", "crossplane-xr-multiversion")
	out, err := RunRehub(RehubOptions{
		ConfigPath: filepath.Join(root, "04-add-v3", "xrdconversionconfig.yaml"),
		XRDPath:    filepath.Join(root, "04-add-v3", "xrd.yaml"),
		To:         "v3",
	})
	if err != nil {
		t.Fatal(err)
	}
	cfg := out.(*teraskyv1alpha1.XRDConversionConfig)
	want, err := LoadConfig(filepath.Join(root, "05-promote-v3", "xrdconversionconfig.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	assertRehubMatches(t, cfg, want)
}

func TestRunRehub_ToNotSpoke(t *testing.T) {
	t.Parallel()
	root := filepath.Join("..", "..", "examples", "crossplane-xr-multiversion")
	_, err := RunRehub(RehubOptions{
		ConfigPath: filepath.Join(root, "02-add-v2", "xrdconversionconfig.yaml"),
		XRDPath:    filepath.Join(root, "02-add-v2", "xrd.yaml"),
		To:         "v9",
	})
	if err == nil {
		t.Fatal("expected error for --to not a spoke")
	}
}

func assertRehubMatches(t *testing.T, got, want *teraskyv1alpha1.XRDConversionConfig) {
	t.Helper()
	if got.Spec.HubVersion != want.Spec.HubVersion {
		t.Fatalf("hubVersion got %q want %q", got.Spec.HubVersion, want.Spec.HubVersion)
	}
	if len(got.Spec.Spokes) != len(want.Spec.Spokes) {
		t.Fatalf("spokes len got %d want %d\ngot=%+v\nwant=%+v", len(got.Spec.Spokes), len(want.Spec.Spokes), got.Spec.Spokes, want.Spec.Spokes)
	}
	wantBy := map[string]teraskyv1alpha1.SpokeVersionRules{}
	for _, s := range want.Spec.Spokes {
		wantBy[s.Version] = s
	}
	for _, gs := range got.Spec.Spokes {
		ws, ok := wantBy[gs.Version]
		if !ok {
			t.Fatalf("unexpected spoke %q", gs.Version)
		}
		if len(gs.Rules) != len(ws.Rules) {
			t.Fatalf("spoke %s rules len got %d want %d\ngot=%+v\nwant=%+v", gs.Version, len(gs.Rules), len(ws.Rules), gs.Rules, ws.Rules)
		}
		// Compare as a multiset of FieldRename pairs (order may differ for lifted rules).
		gotPairs := renamePairs(gs.Rules)
		wantPairs := renamePairs(ws.Rules)
		if len(gotPairs) != len(wantPairs) {
			t.Fatalf("spoke %s rename pairs got %v want %v", gs.Version, gotPairs, wantPairs)
		}
		for k, n := range wantPairs {
			if gotPairs[k] != n {
				t.Fatalf("spoke %s missing/extra rename %s: got %v want %v", gs.Version, k, gotPairs, wantPairs)
			}
		}
	}
}

func renamePairs(rules []teraskyv1alpha1.ConversionRule) map[string]int {
	out := map[string]int{}
	for _, r := range rules {
		if r.Strategy != teraskyv1alpha1.StrategyFieldRename || r.FieldRename == nil {
			out["non-rename:"+string(r.Strategy)]++
			continue
		}
		out[r.FieldRename.HubPath+"→"+r.FieldRename.SpokePath]++
	}
	return out
}
