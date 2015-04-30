package main

import (
	"archive/tar"
	"encoding/json"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh/terminal"
)

var metricWorkers int
var workerErrors bool

// MetricData represents an individual metric and its raw data.
// XXX: Unify this with MetricStatType?
type MetricData struct {
	Name    string
	Size    int64
	Mode    int64
	ModTime int64
	Data    []byte
}

type MetricWork struct {
	Name   string
	Server string
}

func init() {
	usage := "[options] <metric expression>"
	short := "Build a tarball of given metrics."
	long := `Creates a tar archive of the given metrics on STDOUT.

The default mode is to work with lists.  The arguments are a series of one or
more metric key names.  If the first argument is a "-" then read a JSON array
from STDIN as our list of metrics.

Use -r to enable regular expression mode.  The first argument is a regular
expression.  If metrics names match they will be included in the output.

Use -s to only delete metrics found on the server specified by -h or the
BUCKYSERVER environment variable.

Set -w to change the number of worker threads used to download the Whisper
DBs from the remote servers.

The tar archive is written to STDOUT and will not be written to a
terminal.`

	c := NewCommand(tarCommand, "tar", usage, short, long)
	SetupHostname(c)
	SetupSingle(c)
	SetupJSON(c)

	c.Flag.BoolVar(&listForce, "f", false,
		"Force metric re-inventory.")
	c.Flag.BoolVar(&listRegexMode, "r", false,
		"Filter by a regular expression.")
	c.Flag.IntVar(&metricWorkers, "w", 5,
		"Downloader threads.")
	c.Flag.IntVar(&metricWorkers, "workers", 5,
		"Downloader threads.")
}

func GetMetricData(server, name string) (*MetricData, error) {
	httpClient := GetHTTP()
	u := fmt.Sprintf("http://%s:%s/metrics/%s", server, GetBuckyPort(), name)
	r, err := http.NewRequest("GET", u, nil)
	if err != nil {
		log.Printf("Error building request: %s", err)
		return nil, err
	}

	resp, err := httpClient.Do(r)
	if err != nil {
		log.Printf("Error downloading metric data: %s", err)
		return nil, err
	}
	defer resp.Body.Close()

	data := new(MetricData)
	err = json.Unmarshal([]byte(resp.Header.Get("X-Metric-Stat")), &data)
	if err != nil {
		log.Printf("Error unmarshalling X-Metric-Stat header: %s", err)
		return nil, err
	}

	data.Data, err = ioutil.ReadAll(resp.Body)
	if err != nil {
		log.Printf("Error reading response body: %s", err)
		return nil, err
	}

	return data, nil
}

func MetricToPath(metric string) string {
	// assume no prefix
	path := strings.Replace(metric, ".", "/", -1) + ".wsp"
	for path[0] == '/' {
		path = path[1:]
	}

	return path
}

func PathToMetric(path string) string {
	// assume no prefix
	for path[0] == '/' {
		path = path[1:]
	}
	metric := strings.Replace(path, "/", ".", -1)
	metric = metric[:len(metric)-4]
	return metric
}

func writeTar(workOut chan *MetricData, wg *sync.WaitGroup) {
	tw := tar.NewWriter(os.Stdout)
	for work := range workOut {
		log.Printf("Writing %s...", work.Name)
		th := new(tar.Header)
		th.Name = MetricToPath(work.Name)
		th.Size = work.Size
		th.Mode = work.Mode
		th.ModTime = time.Unix(work.ModTime, 0)

		err := tw.WriteHeader(th)
		if err != nil {
			log.Fatal("Error writing tar: %s", err)
		}
		_, err = tw.Write(work.Data)
	}

	err := tw.Close()
	if err != nil {
		log.Fatal("Error closing tar archive: %s", err)
	}

	wg.Done()
}

func getMetricWorker(workIn chan *MetricWork, workOut chan *MetricData, wg *sync.WaitGroup) {
	for w := range workIn {
		metric, err := GetMetricData(w.Server, w.Name)
		if err == nil {
			workOut <- metric
		} else {
			workerErrors = true
		}
	}

	wg.Done()
}

func multiplexTar(metricMap map[string][]string) error {
	wgTar := new(sync.WaitGroup)
	wgWork := new(sync.WaitGroup)
	workIn := make(chan *MetricWork, 25)
	workOut := make(chan *MetricData, 25)

	// Sort our work queue for sanity and balancing across the cluster
	servers := make(map[string]string)
	sorted := make([]string, 0)
	for server, metrics := range metricMap {
		for _, m := range metrics {
			servers[m] = server
			sorted = append(sorted, m)
		}
	}
	sort.Strings(sorted)
	log.Printf("Total metrics selected for tar: %d", len(sorted))

	// Start writers and workers
	wgTar.Add(1)
	go writeTar(workOut, wgTar)

	wgWork.Add(metricWorkers)
	for i := 0; i < metricWorkers; i++ {
		go getMetricWorker(workIn, workOut, wgWork)
	}

	// Feed work in
	for _, m := range sorted {
		work := new(MetricWork)
		work.Name = m
		work.Server = servers[m]
		workIn <- work
	}
	close(workIn)
	wgWork.Wait()

	// All workers are complete, close workOut
	close(workOut)
	wgTar.Wait() // Wait for tar writer to complete

	if workerErrors {
		return fmt.Errorf("Errors building tar file are present.")
	}
	return nil
}

func TarRegexMetrics(servers []string, regex string, force bool) error {
	metricMap, err := ListRegexMetrics(servers, regex, listForce)
	if err != nil {
		return err
	}

	return multiplexTar(metricMap)
}

func TarSliceMetrics(servers []string, metrics []string, force bool) error {
	metricMap, err := ListSliceMetrics(servers, metrics, listForce)
	if err != nil {
		return err
	}

	return multiplexTar(metricMap)
}

func TarJSONMetrics(servers []string, fd io.Reader, force bool) error {
	// Read the JSON from the file-like object
	blob, err := ioutil.ReadAll(fd)
	metrics := make([]string, 0)

	err = json.Unmarshal(blob, &metrics)
	// We could just package this up and query the server, but lets check the
	// JSON is valid first.
	if err != nil {
		log.Printf("Error unmarshalling JSON data: %s", err)
		return err
	}

	return TarSliceMetrics(servers, metrics, force)
}

// tarCommand runs this subcommand.
func tarCommand(c Command) int {
	servers := GetAllBuckyd()
	if servers == nil {
		return 1
	}

	if c.Flag.NArg() == 0 {
		log.Fatal("At least one argument is required.")
	}

	if terminal.IsTerminal(int(os.Stdout.Fd())) {
		log.Fatal("Refusing to write tar file to terminal.")
	}

	var err error
	if listRegexMode && c.Flag.NArg() > 0 {
		err = TarRegexMetrics(servers, c.Flag.Arg(0), listForce)
	} else if c.Flag.Arg(0) != "-" {
		err = TarSliceMetrics(servers, c.Flag.Args(), listForce)
	} else {
		err = TarJSONMetrics(servers, os.Stdin, listForce)
	}

	if err != nil {
		return 1
	}
	return 0
}