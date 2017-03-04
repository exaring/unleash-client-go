package unleash

import (
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/unleash/unleash-client-go/internal/api"
	"net/http"
	"time"
)

type MetricsData struct {
	AppName    string     `json:"appName"`
	InstanceID string     `json:"instanceId"`
	Bucket     api.Bucket `json:"bucket"`
}

type ClientData struct {
	AppName    string    `json:"appName"`
	InstanceID string    `json:"instanceId"`
	Strategies []string  `json:"strategies"`
	Started    time.Time `json:"started"`
	Interval   int64     `json:"interval"`
}

type metric struct {
	Name    string
	Enabled bool
}

type metrics struct {
	metricsChannels
	options      MetricsOptions
	started      time.Time
	bucket       api.Bucket
	countChannel chan metric
	stopped      chan bool
	timer        *time.Timer
}

func newMetrics(options MetricsOptions, channels metricsChannels) *metrics {
	m := &metrics{
		metricsChannels: channels,
		options:         options,
		started:         time.Now(),
		countChannel:    make(chan metric),
		stopped:         make(chan bool),
	}

	if m.options.HttpClient == nil {
		m.options.HttpClient = http.DefaultClient
	}

	m.resetBucket()

	if m.options.MetricsInterval > 0 {
		m.startTimer()
		m.registerInstance()
		go m.sync()
	}

	return m
}

func (m *metrics) Close() error {
	m.stop()
	return nil
}

func (m *metrics) startTimer() {
	if m.options.DisableMetrics {
		return
	}

	m.timer = time.NewTimer(m.options.MetricsInterval)
}

func (m *metrics) stop() {
	if !m.timer.Stop() {
		<-m.timer.C
	}
	m.stopped <- true
}

func (m *metrics) sync() {
	for {
		select {
		case mc := <-m.countChannel:
			t, exists := m.bucket.Toggles[mc.Name]
			if !exists {
				t = api.ToggleCount{}
			}
			if mc.Enabled {
				t.Yes++
			} else {
				t.No++
			}
			m.bucket.Toggles[mc.Name] = t
		case <-m.timer.C:
			m.sendMetrics()
		case <-m.stopped:
			m.options.DisableMetrics = true
			return
		}
	}

}

func (m *metrics) registerInstance() {
	if m.options.DisableMetrics {
		return
	}

	u, _ := m.options.Url.Parse("./client/register")

	var body bytes.Buffer
	payload := m.getClientData()
	enc := json.NewEncoder(&body)
	if err := enc.Encode(payload); err != nil {
		m.err(err)
		return
	}

	req, err := http.NewRequest("POST", u.String(), &body)
	if err != nil {
		m.err(err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.options.HttpClient.Do(req)
	if err != nil {
		m.err(err)
		return
	}

	if resp.StatusCode < http.StatusOK || resp.StatusCode > http.StatusMultipleChoices {
		m.warn(fmt.Errorf("%s return %d", u.String(), resp.StatusCode))
	}

	m.registered <- payload
}

func (m *metrics) sendMetrics() {
	if m.options.DisableMetrics {
		return
	}

	if m.bucket.IsEmpty() {
		m.resetBucket()
		m.startTimer()
		return
	}

	u, _ := m.options.Url.Parse("./client/metrics")

	var body bytes.Buffer
	payload := m.getPayload()
	enc := json.NewEncoder(&body)
	if err := enc.Encode(payload); err != nil {
		m.err(err)
		return
	}

	req, err := http.NewRequest("POST", u.String(), &body)
	m.startTimer()
	if err != nil {
		m.err(err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := m.options.HttpClient.Do(req)
	if err != nil {
		m.err(err)
		return
	}

	if resp.StatusCode == http.StatusNotFound {
		m.warn(fmt.Errorf("%s return 404, stopping metrics", u.String()))
		m.stop()
		return
	}

	if resp.StatusCode < http.StatusOK || resp.StatusCode > http.StatusMultipleChoices {
		m.warn(fmt.Errorf("%s return %d", u.String(), resp.StatusCode))
	}

	m.sent <- payload
}

func (m metrics) count(name string, enabled bool) {
	if m.options.DisableMetrics {
		return
	}
	m.countChannel <- metric{name, enabled}
}

func (m *metrics) resetBucket() {
	m.bucket = api.Bucket{
		Start:   time.Now(),
		Toggles: map[string]api.ToggleCount{},
	}
}

func (m *metrics) closeBucket() {
	m.bucket.Stop = time.Now()
}

func (m *metrics) getPayload() MetricsData {
	m.closeBucket()
	metricsData := m.getMetricsData()
	m.resetBucket()
	return metricsData
}

func (m metrics) getClientData() ClientData {
	return ClientData{
		m.options.AppName,
		m.options.InstanceID,
		m.options.Strategies,
		m.started,
		int64(m.options.MetricsInterval.Seconds()),
	}
}

func (m metrics) getMetricsData() MetricsData {
	return MetricsData{
		m.options.AppName,
		m.options.InstanceID,
		m.bucket,
	}
}
