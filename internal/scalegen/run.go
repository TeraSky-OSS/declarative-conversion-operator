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

package scalegen

import (
	"context"
	"fmt"
	"io"
	"math"
	"sort"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	extv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/dynamic"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1a "github.com/terasky-oss/declarative-conversion-operator/api/v1alpha1"
)

// Options configures a cluster-scale run.
type Options struct {
	Kubeconfig    string
	Namespace     string
	Targets       int
	Instances     int
	StrategiesMin int
	StrategiesMax int
	Seed          int64
	Parallel      int
	Timeout       time.Duration
	ListRepeats   int
	GetRepeats    int
	DryRun        bool
	Out           io.Writer
}

// Stats is latency for one operation class (get or list).
type Stats struct {
	N      int
	Errors int
	P50    time.Duration
	P99    time.Duration
	Max    time.Duration
}

// Result is printed at the end of a run.
type Result struct {
	Targets   int
	Instances int
	Coverage  map[v1a.Strategy]int
	Create    time.Duration
	ListV1    Stats
	ListV2    Stats
	GetV1     Stats
	GetV2     Stats
}

func (o Options) withDefaults() Options {
	if o.Namespace == "" {
		o.Namespace = "dco-scale"
	}
	if o.Targets <= 0 {
		o.Targets = 4
	}
	if o.Instances <= 0 {
		o.Instances = 5
	}
	if o.StrategiesMin <= 0 {
		o.StrategiesMin = 3
	}
	if o.StrategiesMax <= 0 {
		o.StrategiesMax = 10
	}
	if o.Parallel <= 0 {
		o.Parallel = 8
	}
	if o.Timeout <= 0 {
		o.Timeout = 30 * time.Minute
	}
	if o.ListRepeats <= 0 {
		o.ListRepeats = 3
	}
	if o.GetRepeats <= 0 {
		o.GetRepeats = 1
	}
	if o.Out == nil {
		o.Out = io.Discard
	}
	return o
}

func (o Options) logf(format string, args ...any) {
	fmt.Fprintf(o.Out, format+"\n", args...)
}

// Run generates CRDs (3 versions, 3–10 strategies per spoke, all catalog
// strategies used across the fleet), applies them, creates instances, and
// issues parallel Get/List calls at both spoke versions.
func Run(ctx context.Context, opts Options) (*Result, error) {
	opts = opts.withDefaults()
	targets, err := BuildTargets(opts.Targets, opts.StrategiesMin, opts.StrategiesMax, opts.Seed)
	if err != nil {
		return nil, err
	}
	cov := StrategyCoverage(targets)
	opts.logf("generated %d CRDs × 3 versions (hub %s); strategy coverage:", len(targets), HubVersion)
	for _, s := range slots() {
		opts.logf("  %-24s %d", s.Name, cov[s.Name])
	}
	if opts.DryRun {
		return &Result{Targets: len(targets), Instances: opts.Instances, Coverage: cov}, nil
	}

	cfg, err := restConfig(opts.Kubeconfig)
	if err != nil {
		return nil, err
	}
	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		return nil, err
	}
	if err := extv1.AddToScheme(scheme); err != nil {
		return nil, err
	}
	if err := v1a.AddToScheme(scheme); err != nil {
		return nil, err
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return nil, err
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()

	if err := ensureNamespace(ctx, c, opts.Namespace); err != nil {
		return nil, err
	}
	opts.logf("applying %d CRDs", len(targets))
	for _, t := range targets {
		crd := t.CRD.DeepCopy()
		crd.ResourceVersion = ""
		if err := c.Create(ctx, crd); err != nil && !apierrors.IsAlreadyExists(err) {
			return nil, fmt.Errorf("create CRD %s: %w", t.CRDName, err)
		}
	}
	for _, t := range targets {
		if err := waitCRDEstablished(ctx, c, t.CRDName); err != nil {
			return nil, err
		}
	}
	opts.logf("applying %d CRDConversionConfigs", len(targets))
	for _, t := range targets {
		cfgObj := t.Config.DeepCopy()
		cfgObj.ResourceVersion = ""
		if err := c.Create(ctx, cfgObj); err != nil && !apierrors.IsAlreadyExists(err) {
			return nil, fmt.Errorf("create config %s: %w", cfgObj.Name, err)
		}
	}
	for _, t := range targets {
		if err := waitConfigApplied(ctx, c, t.Config.Name); err != nil {
			return nil, err
		}
		if err := waitCRDWebhook(ctx, c, t.CRDName); err != nil {
			return nil, err
		}
	}

	opts.logf("creating %d instances × %d types in %s", opts.Instances, len(targets), opts.Namespace)
	createStart := time.Now()
	createJobs := make([]func() error, 0, len(targets)*opts.Instances)
	for _, t := range targets {
		t := t
		gvr := t.GVR(V1)
		for n := 0; n < opts.Instances; n++ {
			n := n
			createJobs = append(createJobs, func() error {
				obj := t.Instance(opts.Namespace, n)
				create := func() error {
					_, err := dyn.Resource(gvr).Namespace(opts.Namespace).Create(ctx, obj, metav1.CreateOptions{})
					if apierrors.IsAlreadyExists(err) {
						return nil
					}
					return err
				}
				return retryNoMatch(ctx, create)
			})
		}
	}
	if err := runErrPool(ctx, opts.Parallel, createJobs); err != nil {
		return nil, fmt.Errorf("create instances: %w", err)
	}
	createDur := time.Since(createStart)
	opts.logf("created instances in %s", createDur.Round(time.Millisecond))

	opts.logf("benchmarking parallel List/Get at spoke versions %s and %s (%d workers)", V1, V2, opts.Parallel)
	listV1 := benchLists(ctx, dyn, targets, opts.Namespace, V1, opts.ListRepeats, opts.Parallel)
	listV2 := benchLists(ctx, dyn, targets, opts.Namespace, V2, opts.ListRepeats, opts.Parallel)
	getV1 := benchGets(ctx, dyn, targets, opts.Namespace, V1, opts.Instances, opts.GetRepeats, opts.Parallel)
	getV2 := benchGets(ctx, dyn, targets, opts.Namespace, V2, opts.Instances, opts.GetRepeats, opts.Parallel)

	res := &Result{
		Targets: len(targets), Instances: opts.Instances, Coverage: cov,
		Create: createDur, ListV1: listV1, ListV2: listV2, GetV1: getV1, GetV2: getV2,
	}
	printResult(opts.Out, res)
	if listV1.Errors+listV2.Errors+getV1.Errors+getV2.Errors > 0 {
		return res, fmt.Errorf("scale run completed with get/list errors")
	}
	return res, nil
}

func printResult(w io.Writer, r *Result) {
	fmt.Fprintf(w, "\n=== cluster scale ===\n")
	fmt.Fprintf(w, "targets=%d instances/type=%d total objects=%d\n", r.Targets, r.Instances, r.Targets*r.Instances)
	fmt.Fprintf(w, "create: %s\n", r.Create.Round(time.Millisecond))
	printStats(w, "list v1 (hub→spoke, N objects/call)", r.ListV1)
	printStats(w, "list v2 (hub→spoke, N objects/call)", r.ListV2)
	printStats(w, "get  v1 (hub→spoke, 1 object/call)", r.GetV1)
	printStats(w, "get  v2 (hub→spoke, 1 object/call)", r.GetV2)
}

func printStats(w io.Writer, name string, s Stats) {
	fmt.Fprintf(w, "%s: n=%d errors=%d p50=%s p99=%s max=%s\n",
		name, s.N, s.Errors, s.P50.Round(time.Millisecond), s.P99.Round(time.Millisecond), s.Max.Round(time.Millisecond))
}

func restConfig(kubeconfig string) (*rest.Config, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		rules.ExplicitPath = kubeconfig
	}
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, &clientcmd.ConfigOverrides{}).ClientConfig()
}

func ensureNamespace(ctx context.Context, c client.Client, name string) error {
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}}
	if err := c.Create(ctx, ns); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("create namespace %s: %w", name, err)
	}
	return nil
}

func waitCRDEstablished(ctx context.Context, c client.Client, name string) error {
	return wait.PollUntilContextCancel(ctx, time.Second, true, func(ctx context.Context) (bool, error) {
		var crd extv1.CustomResourceDefinition
		if err := c.Get(ctx, client.ObjectKey{Name: name}, &crd); err != nil {
			return false, nil
		}
		for _, cond := range crd.Status.Conditions {
			if cond.Type == extv1.Established && cond.Status == extv1.ConditionTrue {
				return true, nil
			}
		}
		return false, nil
	})
}

func waitConfigApplied(ctx context.Context, c client.Client, name string) error {
	return wait.PollUntilContextCancel(ctx, time.Second, true, func(ctx context.Context) (bool, error) {
		var cfg v1a.CRDConversionConfig
		if err := c.Get(ctx, client.ObjectKey{Name: name}, &cfg); err != nil {
			return false, nil
		}
		return meta.IsStatusConditionTrue(cfg.Status.Conditions, v1a.ConditionApplied), nil
	})
}

func waitCRDWebhook(ctx context.Context, c client.Client, name string) error {
	return wait.PollUntilContextCancel(ctx, 500*time.Millisecond, true, func(ctx context.Context) (bool, error) {
		var crd extv1.CustomResourceDefinition
		if err := c.Get(ctx, client.ObjectKey{Name: name}, &crd); err != nil {
			return false, nil
		}
		return crd.Spec.Conversion != nil && crd.Spec.Conversion.Strategy == extv1.WebhookConverter, nil
	})
}

func retryNoMatch(ctx context.Context, fn func() error) error {
	var err error
	for i := 0; i < 20; i++ {
		err = fn()
		if err == nil || !meta.IsNoMatchError(err) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
	return err
}

type timed struct {
	d   time.Duration
	err error
}

func benchLists(ctx context.Context, dyn dynamic.Interface, targets []Target, ns, version string, repeats, parallel int) Stats {
	jobs := make([]func() timed, 0, len(targets)*repeats)
	for _, t := range targets {
		gvr := t.GVR(version)
		for i := 0; i < repeats; i++ {
			jobs = append(jobs, func() timed {
				start := time.Now()
				_, err := dyn.Resource(gvr).Namespace(ns).List(ctx, metav1.ListOptions{})
				return timed{time.Since(start), err}
			})
		}
	}
	return collectTimed(ctx, parallel, jobs)
}

func benchGets(ctx context.Context, dyn dynamic.Interface, targets []Target, ns, version string, instances, repeats, parallel int) Stats {
	jobs := make([]func() timed, 0, len(targets)*instances*repeats)
	for _, t := range targets {
		gvr := t.GVR(version)
		for n := 0; n < instances; n++ {
			name := fmt.Sprintf("obj-%03d", n)
			for i := 0; i < repeats; i++ {
				jobs = append(jobs, func() timed {
					start := time.Now()
					_, err := dyn.Resource(gvr).Namespace(ns).Get(ctx, name, metav1.GetOptions{})
					return timed{time.Since(start), err}
				})
			}
		}
	}
	return collectTimed(ctx, parallel, jobs)
}

func collectTimed(ctx context.Context, parallel int, jobs []func() timed) Stats {
	out := make([]timed, len(jobs))
	sem := make(chan struct{}, parallel)
	var wg sync.WaitGroup
	for i, job := range jobs {
		i, job := i, job
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case <-ctx.Done():
				out[i] = timed{0, ctx.Err()}
				return
			case sem <- struct{}{}:
			}
			defer func() { <-sem }()
			out[i] = job()
		}()
	}
	wg.Wait()
	return summarize(out)
}

func runErrPool(ctx context.Context, parallel int, jobs []func() error) error {
	sem := make(chan struct{}, parallel)
	errCh := make(chan error, 1)
	var wg sync.WaitGroup
	for _, job := range jobs {
		job := job
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-errCh:
			return err
		default:
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case <-ctx.Done():
				return
			case sem <- struct{}{}:
			}
			defer func() { <-sem }()
			if err := job(); err != nil {
				select {
				case errCh <- err:
				default:
				}
			}
		}()
	}
	wg.Wait()
	select {
	case err := <-errCh:
		return err
	default:
		return nil
	}
}

func summarize(in []timed) Stats {
	ds := make([]time.Duration, 0, len(in))
	errs := 0
	for _, t := range in {
		if t.err != nil {
			errs++
			continue
		}
		ds = append(ds, t.d)
	}
	s := Stats{N: len(in), Errors: errs}
	if len(ds) == 0 {
		return s
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i] < ds[j] })
	s.P50 = pct(ds, 50)
	s.P99 = pct(ds, 99)
	s.Max = ds[len(ds)-1]
	return s
}

func pct(ds []time.Duration, p float64) time.Duration {
	if len(ds) == 0 {
		return 0
	}
	idx := int(math.Round(p / 100 * float64(len(ds)-1)))
	if idx < 0 {
		idx = 0
	}
	if idx >= len(ds) {
		idx = len(ds) - 1
	}
	return ds[idx]
}
