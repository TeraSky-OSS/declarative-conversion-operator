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

// Command scalegen generates a fleet of native CRDs with mixed conversion
// strategies, applies them to a cluster, creates instances, and issues
// parallel Get/List calls through the live conversion webhook.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/terasky-oss/declarative-conversion-operator/internal/scalegen"
)

func main() {
	opts := scalegen.Options{Out: os.Stdout}
	var qps float64
	flag.StringVar(&opts.Kubeconfig, "kubeconfig", "", "kubeconfig path (default: KUBECONFIG / in-cluster)")
	flag.StringVar(&opts.Namespace, "namespace", "dco-scale", "namespace for generated CRs")
	flag.IntVar(&opts.Targets, "targets", 4, "number of CRDs to generate (each has 3 versions)")
	flag.IntVar(&opts.Instances, "instances", 5, "CRs to create per CRD")
	flag.IntVar(&opts.StrategiesMin, "strategies-min", 3, "minimum strategies per spoke conversion")
	flag.IntVar(&opts.StrategiesMax, "strategies-max", 10, "maximum strategies per spoke conversion")
	flag.Int64Var(&opts.Seed, "seed", 1, "RNG seed for strategy assignment")
	flag.IntVar(&opts.Parallel, "parallel", 8, "concurrent create/get/list workers")
	flag.DurationVar(&opts.Timeout, "timeout", 30*time.Minute, "overall run timeout")
	flag.IntVar(&opts.ListRepeats, "list-repeats", 3, "List calls per CRD per spoke version")
	flag.IntVar(&opts.GetRepeats, "get-repeats", 1, "Get calls per instance per spoke version")
	flag.Float64Var(&qps, "qps", 100, "client-go QPS (default 5 is too low for 10k creates)")
	flag.IntVar(&opts.Burst, "burst", 200, "client-go burst")
	flag.BoolVar(&opts.Reset, "reset", false, "delete previously generated CRDs in this group before applying")
	flag.BoolVar(&opts.DryRun, "dry-run", false, "print strategy coverage without talking to a cluster")
	flag.Parse()
	opts.QPS = float32(qps)

	if _, err := scalegen.Run(context.Background(), opts); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "scalegen: %v\n", err)
		os.Exit(1)
	}
}
