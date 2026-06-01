package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"text/tabwriter"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	ctrlcfg "sigs.k8s.io/controller-runtime/pkg/client/config"

	"github.com/vibed-project/vibeD/internal/templatevalidate"
)

// runValidateImages implements `vibed validate-images`: it reports the
// bring-your-own base-image validation status the controller records in the
// vibed-template-validation ConfigMap. It reads from the cluster (kubeconfig),
// so it works from anywhere — the controller does the actual booting/probing
// in-cluster on a loop. Exits non-zero if any slot is invalid (CI-friendly).
func runValidateImages(args []string) int {
	fs := flag.NewFlagSet("validate-images", flag.ExitOnError)
	namespace := fs.String("namespace", "vibed-apps", "Workloads namespace where warm pools + the validation ConfigMap live.")
	_ = fs.Parse(args)

	cfg, err := ctrlcfg.GetConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "validate-images: no kubeconfig: %v\n", err)
		return 2
	}
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		fmt.Fprintf(os.Stderr, "validate-images: build client: %v\n", err)
		return 2
	}

	results, err := templatevalidate.LoadAll(context.Background(), c, *namespace)
	if err != nil {
		fmt.Fprintf(os.Stderr, "validate-images: no validation results yet in %s/%s (is the controller running with --validate-images?): %v\n",
			*namespace, templatevalidate.ConfigMapName, err)
		return 2
	}
	if len(results) == 0 {
		fmt.Println("No template images validated yet.")
		return 0
	}

	sort.Slice(results, func(i, j int) bool { return results[i].Template < results[j].Template })
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "TEMPLATE\tVALID\tIMAGE\tDETAIL")
	anyInvalid := false
	for _, r := range results {
		status := "yes"
		detail := r.Language
		if !r.Valid {
			status = "NO"
			detail = r.Reason
			anyInvalid = true
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", r.Template, status, r.Image, detail)
	}
	tw.Flush()
	if anyInvalid {
		return 1
	}
	return 0
}
