package collector

import (
	"fullerite/metric"

	"strings"

	"time"

	l "github.com/Sirupsen/logrus"

	"github.com/fsouza/go-dockerclient"
)

const (
	mesosTaskID    = "MESOS_TASK_ID"
	endpoint       = "unix:///var/run/docker.sock"
	timeoutChannel = 7
)

// DockerStats collector type.
// previousCPUValues contains the last cpu-usage values per container.
// dockerClient is the client for the Docker remote API.
type DockerStats struct {
	baseCollector
	previousCPUValues map[string]*CPUValues
	dockerClient      *docker.Client
}

// CPUValues struct contains the last cpu-usage values in order to compute properly the current values.
// (see calculateCPUPercent() for more details)
type CPUValues struct {
	totCPU, systemCPU float64
}

// NewDockerStats creates a new Test collector.
func NewDockerStats(channel chan metric.Metric, initialInterval int, log *l.Entry) *DockerStats {
	d := new(DockerStats)

	d.log = log
	d.channel = channel
	d.interval = initialInterval
	d.name = "DockerStats"
	d.previousCPUValues = make(map[string]*CPUValues)
	d.dockerClient, _ = docker.NewClient(endpoint)

	return d
}

// Configure takes a dictionary of values with which the handler can configure itself.
func (d *DockerStats) Configure(configMap map[string]interface{}) {
	d.configureCommonParams(configMap)
}

// Collect iterates on all the docker containers alive and, if possible, collects the correspondent
// memory and cpu statistics.
// For each container a gorutine is started to spin up the collection process.
func (d DockerStats) Collect() {
	containerArray, err := d.dockerClient.ListContainers(docker.ListContainersOptions{All: false})
	if err != nil {
		d.log.Error("Impossible reach the docker client", err)
		return
	}
	results := make(chan int, len(containerArray))
	for _, APIContainer := range containerArray {
		container, err := d.dockerClient.InspectContainer(APIContainer.ID)
		if err != nil {
			d.log.Error("Not possible inspect container", err)
			results <- 0
			continue
		}
		if _, ok := d.previousCPUValues[container.ID]; !ok {
			d.previousCPUValues[container.ID] = new(CPUValues)
		}
		go d.GetDockerContainerInfo(container, results)
	}
	for i := 0; i < len(containerArray); i++ {
		<-results
	}
	close(results)
}

// GetDockerContainerInfo gets container statistics for the given container.
// results is a channel to make possible the synchronization between the main process and the gorutines (wait-notify pattern).
func (d DockerStats) GetDockerContainerInfo(container *docker.Container, results chan<- int) {
	errC := make(chan error, 1)
	statsC := make(chan *docker.Stats, 1)
	done := make(chan bool)

	go func() {
		errC <- d.dockerClient.Stats(docker.StatsOptions{container.ID, statsC, false, done, time.Second * 8})
	}()
	select {
	case stats, ok := <-statsC:
		if !ok {
			select {
			case err := <-errC:
				d.log.Error("Received error from stream channel", err)
				break
			case <-time.After(time.Millisecond * 500):
				break
			}
			errC <- nil
			break
		}
		errC <- nil
		done <- false

		d.BuildMetrics(container, float64(stats.MemoryStats.Usage), float64(stats.MemoryStats.Limit), calculateCPUPercent(d.previousCPUValues[container.ID].totCPU, d.previousCPUValues[container.ID].systemCPU, stats))

		d.previousCPUValues[container.ID].totCPU = float64(stats.CPUStats.CPUUsage.TotalUsage)
		d.previousCPUValues[container.ID].systemCPU = float64(stats.CPUStats.SystemCPUUsage)

		break
	case <-time.After(time.Second * timeoutChannel):
		d.log.Error("Impossible sending metric. Timeout expired for the container", container.ID)
		done <- false
		errC <- nil
		break
	}
	<-errC
	results <- 0
}

// BuildMetrics creates the actual metrics for the given container.
func (d DockerStats) BuildMetrics(container *docker.Container, memUsed, memLimit, cpuPercentage float64) {
	ret := []metric.Metric{
		buildDockerMetric("DockerMemoryUsed", memUsed),
		buildDockerMetric("DockerMemoryLimit", memLimit),
		buildDockerMetric("DockerCpuPercentage", cpuPercentage),
	}
	additionalDimensions := map[string]string{}
	additionalDimensions["container_id"] = container.ID
	res := getServiceDimensions(container)
	for key, value := range res {
		additionalDimensions[key] = value
	}
	metric.AddToAll(&ret, additionalDimensions)

	d.SendMetrics(ret)
}

// SendMetrics writes all the metrics received to the collector channel.
func (d DockerStats) SendMetrics(metrics []metric.Metric) {
	for _, m := range metrics {
		d.Channel() <- m
	}
}

// Function that extracts the service and instance name from mesos id in order to add them as dimensions
// in these metrics.
func getServiceDimensions(container *docker.Container) map[string]string {
	envVars := container.Config.Env

	for _, envVariable := range envVars {
		envArray := strings.Split(envVariable, "=")
		if envArray[0] == mesosTaskID {
			serviceName, instance := getInfoFromMesosTaskID(envArray[1])
			tmp := map[string]string{}
			tmp["service_name"] = serviceName
			tmp["instance_name"] = instance
			return tmp
		}
	}
	return nil
}

func getInfoFromMesosTaskID(taskID string) (serviceName, instance string) {
	varArray := strings.Split(taskID, ".")
	return strings.Replace(varArray[0], "--", "_", -1), strings.Replace(varArray[1], "--", "_", -1)
}

func buildDockerMetric(name string, value float64) (m metric.Metric) {
	m = metric.New(name)
	m.Value = value
	m.AddDimension("collector", "fullerite")
	return m
}

// Function that compute the current cpu usage percentage combining current and last values.
func calculateCPUPercent(previousCPU, previousSystem float64, stats *docker.Stats) float64 {
	var (
		cpuPercent = 0.0
		// calculate the change for the cpu usage of the container in between readings
		cpuDelta = float64(stats.CPUStats.CPUUsage.TotalUsage) - previousCPU
		// calculate the change for the entire system between readings
		systemDelta = float64(stats.CPUStats.SystemCPUUsage) - previousSystem
	)

	if systemDelta > 0.0 && cpuDelta > 0.0 {
		cpuPercent = (cpuDelta / systemDelta) * float64(len(stats.CPUStats.CPUUsage.PercpuUsage)) * 100.0
	}
	return cpuPercent
}
