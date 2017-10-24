package grada

import (
	"errors"
	"sync"
	"time"
)

// ## The data aggregator

// Count is a single time series data tuple, consisting of
// a float64 value N and a timestamp T.
type Count struct {
	N float64
	T time.Time
}

// Metric is a ring buffer of Counts.
type Metric struct {
	m    sync.Mutex
	list []Count
	head int
}

// Add a single value to the ring buffer. When the ring buffer
// is full, every new value overwrites the oldest one.
func (g *Metric) Add(n float64) {
	g.m.Lock()
	defer g.m.Unlock()
	g.list[g.head] = Count{n, time.Now()}
	g.head = (g.head + 1) % len(g.list)
}

// AddWithTime adds a single (value, timestamp) tuple to the ring buffer.
func (g *Metric) AddWithTime(n float64, t time.Time) {
	g.AddCount(Count{n, t})
}

func (g *Metric) AddCount(c Count) {
	g.m.Lock()
	defer g.m.Unlock()
	g.list[g.head] = c
	g.head = (g.head + 1) % len(g.list)
}

func (g *Metric) fetchDatapoints() *[]row {

	g.m.Lock()
	defer g.m.Unlock()
	length := len(g.list)
	head := g.head

	rows := make([]row, length)
	for i := 0; i < length; i++ {
		count := g.list[(i+head)%length]                                // wrap around
		rows = append(rows, row{count.N, count.T.UnixNano() / 1000000}) // need ms
	}
	return &rows
}

// Metrics is a map of all metric buffers, with the key being the target name.
type Metrics struct {
	m      sync.Mutex
	metric map[string]*Metric
}

// Get gets the metric with name "target" from the Metrics map. If a metric of that name
// does not exists in the map, Get returns an error.
func (m *Metrics) Get(target string) (*Metric, error) {
	m.m.Lock()
	mt, ok := m.metric[target]
	m.m.Unlock()
	if !ok {
		return nil, errors.New("no such metric: " + target)
	}
	return mt, nil
}

// Put adds a Metric to the Metrics map. Adding an already existing metric
// is an error.
func (m *Metrics) Put(target string, metric *Metric) error {
	m.m.Lock()
	defer m.m.Unlock()

	_, exists := m.metric[target]
	if exists {
		return errors.New("metric " + target + " already exists")
	}
	m.metric[target] = metric
	return nil
}

// Delete removes a metric from the Metrics map. Deleting a non-existing
// metric is an error.
func (m *Metrics) Delete(target string) error {
	m.m.Lock()
	defer m.m.Unlock()
	_, exists := m.metric[target]
	if !exists {
		return errors.New("cannot delete metric: " + target + " does not exist")
	}
	m.metric[target] = nil
	return nil
}

// Create creates a new Metric with the given target name and buffer size
// and adds it to the Metrics map.
func (m *Metrics) Create(target string, size int) (*Metric, error) {
	metric := &Metric{
		list: make([]Count, size, size),
	}
	m.Put(target, metric)
	return metric, nil
}
