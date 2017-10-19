package main

import (
	"bytes"
	"encoding/json"
	"log"
	"math/rand"
	"net/http"
	"runtime"
	"sync"
	"time"
)

// Query is a `/query` request from Grafana.
//
// All JSON-related structs were generated from the JSON examples
// of the "SimpleJson" data source documentation
// using [JSON-to-Go](https://mholt.github.io/json-to-go/),
// with a little tweaking afterwards.
type Query struct {
	PanelID int `json:"panelId"`
	Range   struct {
		From time.Time `json:"from"`
		To   time.Time `json:"to"`
		Raw  struct {
			From string `json:"from"`
			To   string `json:"to"`
		} `json:"raw"`
	} `json:"range"`
	RangeRaw struct {
		From string `json:"from"`
		To   string `json:"to"`
	} `json:"rangeRaw"`
	Interval   string `json:"interval"`
	IntervalMs int    `json:"intervalMs"`
	Targets    []struct {
		Target string `json:"target"`
		RefID  string `json:"refId"`
		Type   string `json:"type"`
	} `json:"targets"`
	Format        string `json:"format"`
	MaxDataPoints int    `json:"maxDataPoints"`
}

// Row is used in TimeseriesResponse and TableResponse.
// Grafana's JSON contains weird arrays with mixed types!
type Row []interface{}

// TimeseriesResponse is the response to a `/query` request
// if "Type" is set to "timeserie".
// It sends time series data back to Grafana.
type TimeseriesResponse struct {
	Target     string `json:"target"`
	Datapoints []Row  `json:"datapoints"`
}

// TableResponse is the response to send when "Type" is "table".
type Column struct {
	Text string `json:"text"`
	Type string `json:"type"`
}
type TableResponse struct {
	Columns []Column `json:"columns"`
	Rows    []Row    `json:"rows"`
	Type    string   `json:"type"`
}

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

// NewMetric creates a new Metric struct with a target name and
// with a ring buffer of the given size.
func NewMetric(name string, size int) *Metric {
	return &Metric{
		list: make([]Count, size, size),
		head: 0,
	}
}

// Add a single value to the ring buffer. When the ring buffer
// is full, every new value overwrites the oldest one.
func (g *Metric) Add(n float64) {
	g.m.Lock()
	g.list[g.head] = Count{n, time.Now()}
	g.head = (g.head + 1) % len(g.list)
	g.m.Unlock()
}

// Add list adds a complete Count list to the ring buffer.
func (g *Metric) AddList(c []Count) {
	g.m.Lock()
	for _, el := range c {
		g.list[g.head] = el
		g.head = (g.head + 1) % len(g.list)
	}
	g.m.Unlock()
}

// AddWithTime adds a single (value, timestamp) tuple to the ring buffer.
func (g *Metric) AppendWithTime(n float64, t time.Time) {
	g.m.Lock()
	g.list[g.head] = Count{n, t}
	g.head = (g.head + 1) % len(g.list)
	g.m.Unlock()
}

func (g *Metric) fetchMetric() *[]Row {

	g.m.Lock()
	length := len(g.list)
	gcnt := make([]Count, length, length)
	head := g.head
	copy(gcnt, g.list)
	g.m.Unlock()

	rows := []Row{}
	for i := 0; i < length; i++ {
		count := gcnt[(i+head)%length] // wrap around
		rows = append(rows, Row{count.N, count.T.UnixNano() / 1000000})
	}
	return &rows
}

// Metrics is a map of all metric buffers, with the key being the target name.
type Metrics map[string]*Metric

// ## The data generator

func spawnGoroutines() {
	for {
		// Spawn a few dozen goroutines in a burst.
		for i := 0; i < rand.Intn(20)+20; i++ {
			// Each goroutine shall live for a random time between
			// 1 and 100 seconds.
			go func(n int) {
				time.Sleep(time.Duration(n) * time.Second)
			}(rand.Intn(100))
		}
		// Wait for a few seconds between goroutine bursts.
		// The more goroutines exist, the longer the wait.
		time.Sleep(time.Duration(rand.Intn(10)+runtime.NumGoroutine()/10) * time.Second)
	}
}

func newFakeDataFunc(max int, volatility float64) func() int {
	value := rand.Intn(max)
	return func() int {
		rnd := rand.Float64() - 0.5
		changePercent := 2 * volatility * rnd
		value += int(float64(value) * changePercent)
		return value
	}
}

// ## The server

type App struct {
	Metrics *Metrics
}

func writeError(w http.ResponseWriter, e error, m string) {
	w.WriteHeader(http.StatusBadRequest)
	w.Write([]byte("{\"error\": \"" + m + ": " + e.Error() + "\"}"))

}

func (app *App) queryHandler(w http.ResponseWriter, r *http.Request) {
	var q bytes.Buffer

	_, err := q.ReadFrom(r.Body)
	if err != nil {
		writeError(w, err, "Cannot read request body")
		return
	}

	query := &Query{}
	err = json.Unmarshal(q.Bytes(), query)
	if err != nil {
		writeError(w, err, "cannot unmarshal request body")
		return
	}

	// Our example should contain exactly one target.
	target := query.Targets[0].Target

	log.Println("Sending response for target " + target)

	// Depending on the type, we need to send either a timeseries response
	// or a table response.
	switch query.Targets[0].Type {
	case "timeserie":
		app.sendTimeseries(w, query)
	case "table":
		app.sendTable(w, query)
	}
}

func (a *App) searchHandler(w http.ResponseWriter, r *http.Request) {
	var targets []string
	for t, _ := range *(a.Metrics) {
		targets = append(targets, t)
	}
	resp, err := json.Marshal(targets)
	if err != nil {
		writeError(w, err, "cannot marshal targets response")
	}
	w.Write(resp)
}

func (app *App) sendTimeseries(w http.ResponseWriter, q *Query) {

	log.Println("Sending time series data")

	target := q.Targets[0].Target
	response := []TimeseriesResponse{
		{
			Target:     target,
			Datapoints: (*(*app.Metrics)[target].fetchMetric()),
		},
	}

	jsonResp, err := json.Marshal(response)
	if err != nil {
		writeError(w, err, "cannot marshal timeseries response")
	}

	w.Write(jsonResp)

}

func (app *App) sendTable(w http.ResponseWriter, q *Query) {

	log.Println("Sending table data")

	response := []TableResponse{
		{
			Columns: []Column{
				{Text: "Name", Type: "string"},
				{Text: "Value", Type: "number"},
				{Text: "Time", Type: "time"},
			},
			Rows: []Row{
				{"Alpha", rand.Intn(100), float64(int64(time.Now().UnixNano() / 1000000))},
				{"Bravo", rand.Intn(100), float64(int64(time.Now().UnixNano() / 1000000))},
				{"Charlie", rand.Intn(100), float64(int64(time.Now().UnixNano() / 1000000))},
				{"Delta", rand.Intn(100), float64(int64(time.Now().UnixNano() / 1000000))},
			},
			Type: "table",
		},
	}

	jsonResp, err := json.Marshal(response)
	if err != nil {
		writeError(w, err, "cannot marshal table response")
	}

	w.Write(jsonResp)

}

func Start() {

	app := &App{Metrics: &Metrics{}}

	// Grafana expects a "200 OK" status for "/" when testing the connection.
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	http.HandleFunc("/query", app.queryHandler)

	// Start the server.
	log.Println("start grafanago")
	defer log.Println("stop grafanago")
	err := http.ListenAndServe(":3001", nil)
	if err != nil {
		log.Fatalln(err)
	}
}
