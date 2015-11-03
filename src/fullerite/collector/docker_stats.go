package collector

import (
	"fullerite/metric"

	l "github.com/Sirupsen/logrus"

	//"fmt"

	"strings"

	"github.com/fsouza/go-dockerclient"

	"time"
)

const (
	mesosTaskID       = "MESOS_TASK_ID"
	defaultDockerPath = "/sys/fs/cgroup/memory/sysdefault/docker/"
	endpoint          = "unix:///var/run/docker.sock"
	timeoutChannel    = 3
)

// DockerStats collector type
type DockerStats struct {
	baseCollector
	previousCPUValues map[string]*CPUValues
}

// CPUValues struct
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

	return d
}

// Configure this takes a dictionary of values with which the handler can configure itself
func (d *DockerStats) Configure(configMap map[string]interface{}) {
	d.configureCommonParams(configMap)
}

// Collect method
func (d DockerStats) Collect() {
	client, _ := docker.NewClient(endpoint)
	containerArray, _ := client.ListContainers(docker.ListContainersOptions{All: false})
	results := make(chan int, len(containerArray))
	for _, APIContainer := range containerArray {
		container, err := client.InspectContainer(APIContainer.ID)
		if err != nil {
			results <- 0
			continue
		}
		if _, ok := d.previousCPUValues[container.ID]; !ok {
			d.previousCPUValues[container.ID] = new(CPUValues)
		}
		go d.GetDockerContainerInfo(container, client, results)
	}
	for i := 0; i < len(containerArray); i++ {
		<-results
	}
	close(results)
}

// GetDockerContainerInfo method
func (d DockerStats) GetDockerContainerInfo(container *docker.Container, client *docker.Client, results chan<- int) {
	errC := make(chan error, 1)
	statsC := make(chan *docker.Stats, 1)
	done := make(chan bool)

	go func() {
		errC <- client.Stats(docker.StatsOptions{container.ID, statsC, true, done, time.Second * 5})
		//	close(errC)
	}()
	select {
	case stats, ok := <-statsC:
		if !ok {
			errC <- nil
			break
		}
                errC <- nil

		done <- false

		d.BuildMetrics(container, float64(stats.MemoryStats.Usage), float64(stats.MemoryStats.Limit), calculateCPUPercent(d.previousCPUValues[container.ID].totCPU, d.previousCPUValues[container.ID].systemCPU, stats))

		d.previousCPUValues[container.ID].totCPU = float64(stats.CPUStats.CPUUsage.TotalUsage)
		d.previousCPUValues[container.ID].systemCPU = float64(stats.CPUStats.SystemCPUUsage)

		flag := false
		for {
			if !flag {
				select {
				case _, s := <-done:
                                        if !s {
                                            flag = true
                                            break
                                        }
					break
				case <-time.After(time.Second * 1):
					flag = true
					break
				}
			} else {
				break
			}
		}
		//errC <- nil
		break
	case <-time.After(time.Second * timeoutChannel):
		errC <- nil
		break
	}
	_ = <-errC
	//close(done)

	//err := <-errC
	//if err != nil {
	//	fmt.Println(err)
	//}
	results <- 0
}

// BuildMetrics method
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

// SendMetrics method
func (d DockerStats) SendMetrics(metrics []metric.Metric) {
	for _, m := range metrics {
		d.Channel() <- m
	}
}

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
