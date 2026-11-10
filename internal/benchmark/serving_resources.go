package benchmark

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

type servingSample struct {
	At          time.Time          `json:"at"`
	Values      map[string]float64 `json:"values"`
	Unavailable []string           `json:"unavailable,omitempty"`
}

type servingSampler struct {
	client   *servingClient
	pid      int
	identity string
	clock    float64
	nvidia   string
	energy   map[string]float64
	joules   float64
}

func newServingSampler(client *servingClient) (*servingSampler, error) {
	s := &servingSampler{client: client, pid: client.plan.LocalPID, energy: map[string]float64{}}
	if s.pid == 0 {
		return s, nil
	}
	if runtime.GOOS != "linux" {
		return nil, errors.New("local_server_pid sampling requires Linux on the server node")
	}
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", s.pid))
	if err != nil {
		return nil, fmt.Errorf("cannot observe supplied server PID: %w", err)
	}
	fields := processFields(string(stat))
	if len(fields) < 20 {
		return nil, errors.New("malformed server process identity")
	}
	s.identity = fields[19]
	if raw, err := exec.Command("getconf", "CLK_TCK").Output(); err == nil {
		s.clock, _ = strconv.ParseFloat(strings.TrimSpace(string(raw)), 64)
	}
	s.nvidia, _ = exec.LookPath("nvidia-smi")
	return s, nil
}

func processFields(stat string) []string {
	end := strings.LastIndex(stat, ") ")
	if end < 0 {
		return nil
	}
	return strings.Fields(stat[end+2:])
}

func (s *servingSampler) sample(ctx context.Context) (p servingSample) {
	p = servingSample{Values: map[string]float64{}}
	defer func() { p.At = time.Now().UTC() }()
	if s.client.plan.MetricsURL != "" {
		u, _ := url.Parse(s.client.plan.MetricsURL)
		base, _ := url.Parse(s.client.plan.Endpoint)
		token := os.Getenv("MODELGAUGE_INFERENCE_METRICS_TOKEN")
		if token == "" && u.Scheme == base.Scheme && u.Host == base.Host {
			token = os.Getenv("MODELGAUGE_INFERENCE_TOKEN")
		}
		probe, stop := context.WithTimeout(ctx, 2*time.Second)
		r, err := s.client.request(probe, "GET", s.client.plan.MetricsURL, token, nil)
		if err == nil {
			if r.StatusCode == 200 {
				data, e := io.ReadAll(io.LimitReader(r.Body, (1<<20)+1))
				if e == nil && len(data) <= 1<<20 {
					parseServingMetrics(string(data), p.Values)
				} else {
					p.Unavailable = append(p.Unavailable, "endpoint metrics exceeded limit or could not be read")
				}
			} else {
				p.Unavailable = append(p.Unavailable, fmt.Sprintf("endpoint metrics HTTP %d", r.StatusCode))
			}
			r.Body.Close()
		} else {
			p.Unavailable = append(p.Unavailable, "endpoint metrics unreachable")
		}
		stop()
	}
	if s.pid == 0 {
		return p
	}
	root := fmt.Sprintf("/proc/%d", s.pid)
	data, err := os.ReadFile(root + "/stat")
	fields := processFields(string(data))
	if err != nil || len(fields) < 20 || fields[19] != s.identity {
		p.Unavailable = append(p.Unavailable, "supplied server PID exited or changed; replacement process is not sampled")
		return p
	}
	for key, index := range map[string]int{"process_minor_faults": 7, "process_major_faults": 9} {
		if v, ok := parseServingNumber(fields[index]); ok {
			p.Values[key] = v
		}
	}
	if s.clock > 0 {
		a, okA := parseServingNumber(fields[11])
		b, okB := parseServingNumber(fields[12])
		if okA && okB {
			p.Values["process_cpu_seconds"] = (a + b) / s.clock
		}
	} else {
		p.Unavailable = append(p.Unavailable, "process CPU clock tick frequency")
	}
	readProcValues(root+"/status", map[string]string{"VmRSS": "process_rss_bytes", "VmHWM": "process_lifetime_peak_rss_bytes", "voluntary_ctxt_switches": "process_voluntary_context_switches", "nonvoluntary_ctxt_switches": "process_involuntary_context_switches"}, p.Values)
	readProcValues(root+"/smaps_rollup", map[string]string{"Pss": "process_pss_bytes"}, p.Values)
	readProcValues(root+"/io", map[string]string{"read_bytes": "process_read_bytes", "write_bytes": "process_write_bytes"}, p.Values)
	readProcValues("/proc/meminfo", map[string]string{"MemAvailable": "node_memory_available_bytes", "SwapTotal": "node_swap_total_bytes", "SwapFree": "node_swap_free_bytes"}, p.Values)
	if total, ok := p.Values["node_swap_total_bytes"]; ok {
		p.Values["node_swap_used_bytes"] = total - p.Values["node_swap_free_bytes"]
	}
	readProcValues("/proc/vmstat", map[string]string{"pswpin": "node_swapin_pages", "pswpout": "node_swapout_pages"}, p.Values)
	if freq, ok := averageFiles("/sys/devices/system/cpu/cpu*/cpufreq/scaling_cur_freq"); ok {
		p.Values["node_cpu_frequency_mhz"] = freq / 1000
	} else {
		p.Unavailable = append(p.Unavailable, "CPU frequency")
	}
	if temp, ok := maxFiles("/sys/class/hwmon/hwmon*/temp*_input"); ok {
		p.Values["node_max_sensor_temperature_celsius"] = temp / 1000
	} else {
		p.Unavailable = append(p.Unavailable, "node temperature sensors")
	}
	paths, _ := filepath.Glob("/sys/class/powercap/intel-rapl:*/energy_uj")
	hasEnergy := false
	for _, path := range paths {
		if strings.Count(filepath.Base(filepath.Dir(path)), ":") != 1 {
			continue
		}
		value, ok := readNumber(path)
		if !ok {
			continue
		}
		hasEnergy = true
		if previous, exists := s.energy[path]; exists {
			delta := value - previous
			if delta < 0 {
				if limit, ok := readNumber(filepath.Join(filepath.Dir(path), "max_energy_range_uj")); ok {
					delta += limit
				}
			}
			if delta >= 0 {
				s.joules += delta / 1e6
			}
		}
		s.energy[path] = value
	}
	if hasEnergy {
		p.Values["node_cpu_package_energy_joules_total"] = s.joules
	} else {
		p.Unavailable = append(p.Unavailable, "readable CPU-package energy counters")
	}
	if s.nvidia != "" {
		probe, stop := context.WithTimeout(ctx, 2*time.Second)
		raw, err := exec.CommandContext(probe, s.nvidia, "--query-gpu=memory.used,memory.total,utilization.gpu,power.draw,temperature.gpu,clocks.sm", "--format=csv,noheader,nounits").Output()
		stop()
		if err == nil {
			for _, line := range strings.Split(string(raw), "\n") {
				fields := strings.Split(line, ",")
				if len(fields) != 6 {
					continue
				}
				for i, key := range []string{"gpu_memory_used_bytes", "gpu_memory_total_bytes", "gpu_utilization_percent", "gpu_power_watts", "gpu_temperature_celsius", "gpu_clock_mhz"} {
					if v, ok := parseServingNumber(strings.TrimSpace(fields[i])); ok {
						if i < 2 {
							v *= 1024 * 1024
						}
						if i < 2 || i == 3 {
							p.Values[key] += v
						} else {
							p.Values[key] = max(p.Values[key], v)
						}
					}
				}
			}
		} else {
			p.Unavailable = append(p.Unavailable, "NVIDIA GPU telemetry")
		}
	} else {
		p.Unavailable = append(p.Unavailable, "NVIDIA GPU telemetry (nvidia-smi unavailable)")
	}
	return p
}

func readProcValues(path string, keys map[string]string, out map[string]float64) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		key, ok := keys[strings.TrimSuffix(fields[0], ":")]
		if !ok {
			continue
		}
		if value, ok := parseServingNumber(fields[1]); ok {
			if len(fields) > 2 && fields[2] == "kB" {
				value *= 1024
			}
			out[key] = value
		}
	}
}

func readNumber(path string) (float64, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	return parseServingNumber(strings.TrimSpace(string(data)))
}
func averageFiles(pattern string) (float64, bool) {
	paths, _ := filepath.Glob(pattern)
	total, count := 0.0, 0
	for _, path := range paths {
		if v, ok := readNumber(path); ok {
			total += v
			count++
		}
	}
	if count == 0 {
		return 0, false
	}
	return total / float64(count), true
}
func maxFiles(pattern string) (float64, bool) {
	paths, _ := filepath.Glob(pattern)
	largest := math.Inf(-1)
	for _, path := range paths {
		if v, ok := readNumber(path); ok {
			largest = max(largest, v)
		}
	}
	return largest, !math.IsInf(largest, -1)
}

func parseServingMetrics(raw string, values map[string]float64) {
	allowed := map[string]bool{}
	for _, name := range strings.Fields(`llamacpp:prompt_tokens_total llamacpp:prompt_seconds_total llamacpp:prompt_tokens_seconds llamacpp:tokens_predicted_total llamacpp:tokens_predicted_seconds_total llamacpp:predicted_tokens_seconds llamacpp:requests_processing llamacpp:requests_deferred llamacpp:n_tokens_max llamacpp:n_decode_total llamacpp:n_busy_slots_per_decode vllm:num_requests_running vllm:num_requests_waiting vllm:kv_cache_usage_perc vllm:gpu_cache_usage_perc vllm:cpu_cache_usage_perc vllm:prefix_cache_hits vllm:prefix_cache_queries vllm:gpu_prefix_cache_hits vllm:gpu_prefix_cache_queries vllm:request_queue_time_seconds_sum vllm:request_queue_time_seconds_count vllm:time_to_first_token_seconds_sum vllm:time_to_first_token_seconds_count process_resident_memory_bytes process_virtual_memory_bytes process_cpu_seconds_total DCGM_FI_DEV_FB_USED DCGM_FI_DEV_FB_FREE DCGM_FI_DEV_GPU_UTIL DCGM_FI_DEV_POWER_USAGE DCGM_FI_DEV_GPU_TEMP DCGM_FI_DEV_SM_CLOCK`) {
		allowed[name] = true
	}

	scanner := bufio.NewScanner(strings.NewReader(raw))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		nameEnd := strings.IndexAny(line, "{ \t")
		if nameEnd < 0 {
			continue
		}
		name := line[:nameEnd]
		if !allowed[name] {
			continue
		}
		rest := strings.TrimSpace(line[nameEnd:])
		if strings.HasPrefix(rest, "{") {
			// Parse braces outside quoted labels, without retaining sensitive labels.
			quoted, escaped, end := false, false, -1
			for i, ch := range rest {
				if escaped {
					escaped = false
					continue
				}
				if ch == '\\' && quoted {
					escaped = true
					continue
				}
				if ch == '"' {
					quoted = !quoted
				}
				if ch == '}' && !quoted {
					end = i
					break
				}
			}
			if end < 0 {
				continue
			}
			rest = strings.TrimSpace(rest[end+1:])
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		v, ok := parseServingNumber(fields[0])
		if !ok {
			continue
		}
		key := "server:" + name
		if strings.Contains(name, "requests_processing") || strings.Contains(name, "requests_deferred") || strings.Contains(name, "num_requests_running") || strings.Contains(name, "num_requests_waiting") {
			values[key] += v
		} else if old, exists := values[key]; !exists || v > old {
			values[key] = v
		}
	}
}

func summarizeServingResources(samples []servingSample, successful, outputTokens int) ServingResources {
	r := ServingResources{Scope: "endpoint metric gauges (max across label sets; queue gauges summed); local PID/node observations only when explicitly supplied", Samples: len(samples), Observed: map[string]Distribution{}, Unavailable: []string{}}
	if len(samples) > 1 {
		r.ObservationSeconds = samples[len(samples)-1].At.Sub(samples[0].At).Seconds()
	}
	values := map[string][]float64{}
	unavailable := map[string]bool{}
	for _, sample := range samples {
		for key, v := range sample.Values {
			values[key] = append(values[key], v)
		}
		for _, reason := range sample.Unavailable {
			unavailable[reason] = true
		}
	}
	for key, list := range values {
		if d := distribution(list); d != nil {
			r.Observed[key] = *d
		}
	}
	for reason := range unavailable {
		r.Unavailable = append(r.Unavailable, reason)
	}
	sort.Strings(r.Unavailable)
	peak := func(key string) *float64 {
		if d, ok := r.Observed[key]; ok {
			return floatPointer(d.Max)
		}
		return nil
	}
	r.SampledPeakRSSBytes, r.SampledPeakPSSBytes, r.LifetimePeakRSSBytes = peak("process_rss_bytes"), peak("process_pss_bytes"), peak("process_lifetime_peak_rss_bytes")
	if len(samples) > 0 {
		if value, ok := samples[0].Values["process_rss_bytes"]; ok {
			r.InitialRSSBytes = floatPointer(value)
		}
		if value, ok := samples[len(samples)-1].Values["process_rss_bytes"]; ok {
			r.FinalRSSBytes = floatPointer(value)
		}
	}
	delta := func(key string) *float64 {
		list := values[key]
		if len(list) < 2 || len(list) != len(samples) || list[len(list)-1] < list[0] {
			return nil
		}
		return floatPointer(list[len(list)-1] - list[0])
	}
	r.CPUSeconds, r.ReadBytes, r.WriteBytes = delta("process_cpu_seconds"), delta("process_read_bytes"), delta("process_write_bytes")
	r.MajorFaults, r.MinorFaults = delta("process_major_faults"), delta("process_minor_faults")
	if a, b := delta("process_voluntary_context_switches"), delta("process_involuntary_context_switches"); a != nil && b != nil {
		r.ContextSwitches = floatPointer(*a + *b)
	}
	r.NodeEnergyJoules = delta("node_cpu_package_energy_joules_total")
	if r.CPUSeconds != nil && successful > 0 {
		r.CPUSecondsPerRequest = floatPointer(*r.CPUSeconds / float64(successful))
	}
	if r.NodeEnergyJoules != nil && outputTokens > 0 {
		r.JoulesPerOutputToken = floatPointer(*r.NodeEnergyJoules / float64(outputTokens))
	}
	if r.CPUSeconds != nil && len(samples) > 1 {
		elapsed := samples[len(samples)-1].At.Sub(samples[0].At).Seconds()
		if elapsed > 0 {
			r.AverageCPUPercent = floatPointer(*r.CPUSeconds / elapsed * 100)
		}
	}
	energy, intervals := 0.0, 0
	for i := 1; i < len(samples); i++ {
		a, okA := samples[i-1].Values["gpu_power_watts"]
		b, okB := samples[i].Values["gpu_power_watts"]
		dt := samples[i].At.Sub(samples[i-1].At).Seconds()
		if okA && okB && dt > 0 {
			energy += (a + b) / 2 * dt
			intervals++
		}
	}
	if intervals > 0 && intervals == len(samples)-1 {
		r.GPUEnergyJoules = floatPointer(energy)
		if outputTokens > 0 {
			r.GPUJoulesPerOutputToken = floatPointer(energy / float64(outputTokens))
		}
	}
	return r
}
