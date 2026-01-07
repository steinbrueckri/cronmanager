package main

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// isDelayed: Used to signal that the cron job delay was triggered
var (
	jobStartTime time.Time
	jobDuration  float64
	flgVersion   bool
	version      string
	writeMutex   sync.Mutex
)

const (
	idleForSeconds = 60
)

// wait for rest of the idleForSeconds so Prometheus can notice that something is happening
func idleWait(jobStart time.Time) {
	// Idling to let Prometheus to notice we are running
	diff := idleForSeconds - (time.Now().Unix() - jobStart.Unix())
	if diff > 0 {
		fmt.Printf("Idle flag active so I am going to wait for for additional %d seconds", diff)
		time.Sleep(time.Second * time.Duration(diff))
	}
}

func main() {

	version = "1.1.18"
	idle := flag.Bool("i", false, fmt.Sprintf("Idle for %d seconds at the beginning so Prometheus can notice it's actually running", idleForSeconds))
	cmdPtr := flag.String("c", "", "[Required] The `cron job` command")
	jobnamePtr := flag.String("n", "", "[Required] The `job name` to appear in the alarm")
	logfilePtr := flag.String("l", "", "[Optional] The `log file` to store the cron output")
	timeoutPtr := flag.Int("t", 3600, "[Optional] The timeout in `seconds` after which the job is considered delayed (default: 3600)")
	flag.BoolVar(&flgVersion, "version", false, "if true print version and exit")
	flag.Parse()
	if flgVersion {
		fmt.Println("CronManager version " + version)
		os.Exit(0)
	}
	flag.Usage = func() {
		fmt.Printf("Usage: cronmanager -c command  -n jobname  [ -t timeout ] [ -l log file ]\nExample: cronmanager -c \"/usr/bin/php /var/www/app.zlien.com/console broadcast:entities:updated -e project -l 20000\" -n update_entitites_cron -t 3600 -l /path/to/log\n")
		flag.PrintDefaults()
	}
	if *cmdPtr == "" || *jobnamePtr == "" {
		flag.Usage()
		os.Exit(1)
	}
	if *timeoutPtr <= 0 {
		fmt.Fprintf(os.Stderr, "Error: timeout must be greater than 0\n")
		os.Exit(1)
	}

	// Record the start time of the job
	jobStartTime = time.Now()
	timeoutSeconds := *timeoutPtr
	// Start a ticker in a goroutine that will write an alarm metric if the job exceeds the time
	go func() {
		for range time.Tick(time.Second) {
			jobDuration = time.Since(jobStartTime).Seconds()
			// Log current duration counter
			writeToExporter(*jobnamePtr, "duration", strconv.FormatFloat(jobDuration, 'f', 0, 64))
			// Store last timestamp
			writeToExporter(*jobnamePtr, "last", fmt.Sprintf("%d", time.Now().Unix()))
			// Check if job is delayed
			if jobDuration > float64(timeoutSeconds) {
				writeToExporter(*jobnamePtr, "delayed", "1")
			}
		}
	}()

	// Job started
	writeToExporter(*jobnamePtr, "run", "1")
	// Initialize delayed flag to 0
	writeToExporter(*jobnamePtr, "delayed", "0")

	// Parse the command by extracting the first token as the command and the rest as its args
	cmdArr := strings.Split(*cmdPtr, " ")
	if len(cmdArr) == 0 {
		log.Fatal("Error: command cannot be empty")
	}
	cmdBin := cmdArr[0]
	cmdArgs := cmdArr[1:]

	// Validate that the command binary exists and is executable
	if _, err := os.Stat(cmdBin); err != nil {
		log.Fatalf("Error: command binary '%s' not found or not accessible: %v", cmdBin, err)
	}

	// #nosec G204 -- This is the intended purpose of this tool: execute user-provided commands
	cmd := exec.Command(cmdBin, cmdArgs...)

	var buf bytes.Buffer
	var wg sync.WaitGroup

	// If we have a log file specified, use it
	if *logfilePtr != "" {
		outfile, err := os.OpenFile(*logfilePtr, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		if err != nil {
			panic(err)
		}
		defer func() {
			if err := outfile.Close(); err != nil {
				log.Printf("Error closing log file: %v", err)
			}
		}()
		stdoutPipe, err := cmd.StdoutPipe()
		if err != nil {
			panic(err)
		}
		stderrPipe, err := cmd.StderrPipe()
		if err != nil {
			panic(err)
		}
		writer := bufio.NewWriter(outfile)
		defer func() {
			if err := writer.Flush(); err != nil {
				log.Printf("Error flushing writer: %v", err)
			}
		}()
		// Copy both stdout and stderr to the log file
		wg.Add(2)
		go func() {
			defer wg.Done()
			if _, err := io.Copy(writer, stdoutPipe); err != nil {
				log.Printf("Error copying stdout: %v", err)
			}
		}()
		go func() {
			defer wg.Done()
			if _, err := io.Copy(writer, stderrPipe); err != nil {
				log.Printf("Error copying stderr: %v", err)
			}
		}()
	} else {
		cmd.Stdout = &buf
		cmd.Stderr = &buf
	}
	if err := cmd.Start(); err != nil {
		panic(fmt.Sprintf("Failed to start command: %v", err))
	}

	// Execute the command
	err := cmd.Wait()

	// Wait for both pipes to complete after cmd.Wait()
	wg.Wait()

	// wait if idle is active
	if *idle {
		idleWait(jobStartTime)
	}

	if err != nil {
		if exiterr, ok := err.(*exec.ExitError); ok {
			if _, ok := exiterr.Sys().(syscall.WaitStatus); ok {
				writeToExporter(*jobnamePtr, "failed", "1")
				// Job is no longer running
				writeToExporter(*jobnamePtr, "run", "0")
				// Clear delayed flag
				writeToExporter(*jobnamePtr, "delayed", "0")
			}
		} else {
			log.Fatalf("cmd.Wait: %v", err)
		}
	} else {
		// The job had no errors
		writeToExporter(*jobnamePtr, "failed", "0")
		// Job is no longer running
		writeToExporter(*jobnamePtr, "run", "0")
		// Clear delayed flag
		writeToExporter(*jobnamePtr, "delayed", "0")
	}

	// Store last timestamp
	writeToExporter(*jobnamePtr, "last", fmt.Sprintf("%d", time.Now().Unix()))
}

func getExporterPath(jobName string) string {
	exporterPath, exists := os.LookupEnv("COLLECTOR_TEXTFILE_PATH")
	exporterPath = exporterPath + "/" + jobName + ".prom"
	if !exists {
		exporterPath = "/var/lib/node_exporter/" + jobName + ".prom"
	}
	return exporterPath
}

func writeToExporter(jobName string, label string, metric string) {
	// Lock to prevent race conditions when multiple goroutines write to the same file
	writeMutex.Lock()
	defer writeMutex.Unlock()

	jobNeedle := "cronjob{name=\"" + jobName + "\",dimension=\"" + label + "\"}"
	// both TYPE and HELP must be the same across all .prom files
	// otherwise node_exporter textfile won't merge them
	// see https://github.com/prometheus/node_exporter/issues/1885
	helpData := "# HELP cronjob metric generated by cronmanager"
	typeData := "# TYPE cronjob gauge"
	jobData := jobNeedle + " " + metric

	// first the prom file is created in /tmp and then moved to /var/lib/node_exporter
	tmpExporterPath := "/tmp/" + jobName + ".prom"
	finalExporterPath := getExporterPath(jobName)

	// #nosec G304 -- finalExporterPath is derived from COLLECTOR_TEXTFILE_PATH env var or default system path
	input, err := os.ReadFile(finalExporterPath)
	if err != nil {
		// File doesn't exist yet, start with empty input
		input = []byte{}
	}

	// Escape special regex characters in jobNeedle for safe pattern matching
	escapedJobNeedle := regexp.QuoteMeta(jobNeedle)
	re := regexp.MustCompile(escapedJobNeedle + `.*\n`)

	// If we have the job data already, just replace it and that's it
	if re.Match(input) {
		input = re.ReplaceAll(input, []byte(jobData+"\n"))
	} else {
		// If TYPE line is not there then this is the first run of the job
		typeRegex := regexp.MustCompile(`# TYPE cronjob gauge`)
		if !typeRegex.Match(input) {
			// Add HELP, TYPE and the job data
			input = append(input, []byte(helpData+"\n")...)
			input = append(input, []byte(typeData+"\n")...)
			input = append(input, []byte(jobData+"\n")...)
		} else {
			// There is already a TYPE header with one or more other jobs
			// Just append the job data after the TYPE line
			input = append(input, []byte(jobData+"\n")...)
		}
	}

	// #nosec G304 -- tmpExporterPath is constructed from job name and /tmp directory
	f, err := os.Create(tmpExporterPath)
	if err != nil {
		log.Printf("Failed to create temp file: %v", err)
		return
	}
	defer func() {
		if err := f.Close(); err != nil {
			log.Printf("Error closing temp file: %v", err)
		}
	}()

	err = f.Chmod(0644)
	if err != nil {
		log.Printf("Failed to set file permissions: %v", err)
		return
	}

	if _, err = f.Write(input); err != nil {
		log.Printf("Failed to write to temp file: %v", err)
		return
	}

	if err = os.Rename(tmpExporterPath, finalExporterPath); err != nil {
		log.Printf("Failed to move temp file to final location: %v", err)
	}
}
