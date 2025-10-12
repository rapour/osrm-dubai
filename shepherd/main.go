// main.go
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"syscall"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/client-go/rest"

	"k8s.io/client-go/kubernetes"
	corev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	metrics "k8s.io/metrics/pkg/client/clientset/versioned"
)

type Config struct {
	LocustURL        string
	SpawnRate        int
	CPUTargetPercent float64
	PollInterval     time.Duration
	Kubeconfig       string
	HysteresisDelta  float64
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "ERROR:", err)
		os.Exit(1)
	}
}

func getAverageNodeCPUPercent(ctx context.Context, metricsClient *metrics.Clientset, coreClient corev1.CoreV1Interface) (float64, error) {
	// list all node metrics
	nmList, err := metricsClient.MetricsV1beta1().NodeMetricses().List(ctx, metav1.ListOptions{})
	if err != nil {
		return -1, fmt.Errorf("failed to list node metrics: %w", err)
	}
	if len(nmList.Items) == 0 {
		return -1, fmt.Errorf("no node metrics found")
	}

	nodes, err := coreClient.Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return -1, fmt.Errorf("failed to list nodes: %w", err)
	}

	// map nodeName → capacityMilli
	capMap := map[string]int64{}
	for _, n := range nodes.Items {
		if capQ, ok := n.Status.Capacity["cpu"]; ok {
			capMap[n.Name] = capQ.MilliValue()
		}
	}

	var totalUsage, totalCap int64
	for _, nm := range nmList.Items {
		usage := nm.Usage["cpu"]
		cap := capMap[nm.Name]
		totalUsage += usage.MilliValue()
		totalCap += cap
	}

	if totalCap == 0 {
		return -1, fmt.Errorf("total node CPU capacity is zero")
	}

	avg := (float64(totalUsage) / float64(totalCap)) * 100.0
	return avg, nil
}

// map CPU percent (float) to users between min and max with linear mapping and clamp.
func CPUToUserDiff(cpuPct float64, cfg Config) int {

	if cpuPct <= cfg.CPUTargetPercent-cfg.HysteresisDelta {
		return -1
	}
	if cpuPct >= cfg.CPUTargetPercent+cfg.HysteresisDelta {
		return +1
	}

	return 0
}

// send request to Locust master to set users
func setLocustUsers(locustURL string, users, spawnRate int) error {
	data := url.Values{}
	data.Set("user_count", fmt.Sprintf("%d", users))
	data.Set("spawn_rate", fmt.Sprintf("%d", spawnRate))

	resp, err := http.PostForm(locustURL+"/swarm", data)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unexpected status %d from locust /swarm", resp.StatusCode)
	}
	return nil
}

func main() {
	var c Config
	flag.StringVar(&c.LocustURL, "locust-url", "http://locust-master:8089", "base URL of Locust master")
	flag.IntVar(&c.SpawnRate, "spawn-rate", 10, "spawn rate per second when increasing users")
	flag.Float64Var(&c.CPUTargetPercent, "cpu-target", 80.0, "CPU percent mapped to max users")
	flag.DurationVar(&c.PollInterval, "poll-interval", 15*time.Second, "how often to poll metrics")
	flag.Float64Var(&c.HysteresisDelta, "hysteresis", 5.0, "hysteresis percent to avoid flapping")
	flag.Parse()

	restCfg, err := rest.InClusterConfig()
	must(err)

	clientset, err := kubernetes.NewForConfig(restCfg)
	must(err)
	metricsClient, err := metrics.NewForConfig(restCfg)
	must(err)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// handle signals
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		fmt.Println("shutting down")
		cancel()
	}()

	var lastSetUsers int = 0

	ticker := time.NewTicker(c.PollInterval)
	defer ticker.Stop()

	// main loop
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			cpuVal, err := getAverageNodeCPUPercent(ctx, metricsClient, clientset.CoreV1())
			if err != nil {
				fmt.Println("metrics error:", err)
				continue
			}

			desired := lastSetUsers + CPUToUserDiff(cpuVal, c)

			err = setLocustUsers(c.LocustURL, desired, c.SpawnRate)
			if err != nil {
				fmt.Println("failed to set locust users:", err)
				// don't change lastSetUsers so we retry next tick
			} else {
				fmt.Printf("set locust users -> %d\n", desired)
				lastSetUsers = desired
			}

		}
	}
}
