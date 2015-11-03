package collector

import (
	"fullerite/metric"

	"test_utils"

	"testing"

	"time"

	"github.com/stretchr/testify/assert"
)

func TestDockerStatsConfigureEmptyConfig(t *testing.T) {
	config := make(map[string]interface{})

	ps := NewDockerStats(nil, 123, nil)
	ps.Configure(config)

	assert.Equal(t,
		f.Interval(),
		123,
		"should be the default collection interval",
	)
}

func TestDockerStatsConfigure(t *testing.T) {
	config := make(map[string]interface{})
	config["interval"] = 9999

	f := NewDockerStats(nil, 123, nil)
	f.Configure(config)

	assert.Equal(t,
		f.Interval(),
		9990,
		"should be the defined interval",
	)
}

func TestDockerStatsCollect(t *testing.T) {
	config := make(map[string]interface{})

	testChannel := make(chan metric.Metric)
	testLog := test_utils.BuildLogger()

	f := NewDockerStats(testChannel, 123, testLog)
	f.Configure(config)

	go f.Collect()

	select {
	case <-f.Channel():
		return
	case <-time.After(2 * time.Second):
		t.Fail()
	}
}
